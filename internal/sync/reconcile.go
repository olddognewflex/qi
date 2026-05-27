package sync

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"qi/internal/config"
	"qi/internal/domain"
	"qi/internal/index"
	"qi/internal/vault"
)

// maxRetries bounds how many times a single Reconcile re-gathers and retries
// after a TOCTOU abort (ErrConcurrentModification) before giving up on a file.
const maxRetries = 3

// Report summarizes a reconcile for the command layer.
type Report struct {
	// Files maps an absolute file path to the change applied to it.
	Files map[string]FileChange
	// Conflicts lists ids kept as keep-both with a #sync-conflict tag.
	Conflicts []Conflict
	// Skipped lists files abandoned after repeated TOCTOU aborts.
	Skipped []string
	// DryRun indicates no files were written and base was not committed.
	DryRun bool
}

// FileChange counts the line-level delta written to a file (relative to what
// was on disk at gather), for human-readable reporting.
type FileChange struct {
	Path    string
	Added   int
	Removed int
	Total   int // total task lines after the write
}

// Indexer is the subset of *index.Indexer that Reconcile needs. Declared as an
// interface so tests can inject a real temp-backed indexer (or a fake).
type Indexer interface {
	LoadSyncState() (map[string]index.SyncBase, error)
	CommitSyncState(upserts []index.SyncBase, deletes []string) error
}

// fileState is a canon/projection file read at gather: its absolute path, the
// mtime captured when read, the tasks parsed from it, and (for projection
// files) the owning project that ID-less lines are forced to.
type fileState struct {
	path  string
	mtime time.Time
	tasks []domain.Task
}

// Reconcile runs one global bidirectional sync. On TOCTOU abort it re-gathers
// and retries up to maxRetries; a file that keeps racing is reported in
// Report.Skipped and left untouched. When dryRun is true it computes and reports
// the plan without writing files or committing base.
func Reconcile(cfg config.Config, idx Indexer, dryRun bool) (Report, error) {
	tasksDir := filepath.Dir(cfg.TaskFilePath)

	for attempt := 0; ; attempt++ {
		rep, retry, err := reconcileOnce(cfg, idx, tasksDir, dryRun)
		if err != nil {
			return Report{}, err
		}
		if !retry {
			return rep, nil
		}
		if attempt+1 >= maxRetries {
			// Give up: report the racing files as skipped. reconcileOnce already
			// wrote what it could before the abort; surface the abort target.
			rep.DryRun = dryRun
			return rep, nil
		}
	}
}

// reconcileOnce performs a single gather->decide->render->write->commit pass.
// It returns retry=true (with a partial Report listing the racing file in
// Skipped) when a write hit ErrConcurrentModification and the caller should
// re-gather. Writes that succeeded before the abort are durable; base is only
// committed when no abort occurred, so a mid-pass abort leaves base stale and
// the retry re-derives identical decisions.
func reconcileOnce(cfg config.Config, idx Indexer, tasksDir string, dryRun bool) (Report, bool, error) {
	// --- GATHER ---
	canonFiles, err := gatherCanonFiles(tasksDir)
	if err != nil {
		return Report{}, false, err
	}
	projFiles, err := gatherProjectionFiles(cfg.ProjectVaults)
	if err != nil {
		return Report{}, false, err
	}

	canon := make(map[string]domain.Task)
	for _, fs := range canonFiles {
		for _, t := range fs.tasks {
			canon[t.ID] = t
		}
	}

	// proj: id->task, project forced to the owning vault; id-less lines minted.
	proj := make(map[string]domain.Task)
	for _, pv := range cfg.ProjectVaults {
		fs := projFiles[pv.File]
		for _, t := range fs.tasks {
			if t.ID == "" {
				t.ID = vault.MintID()
			}
			t = forceProject(t, pv.Project)
			proj[t.ID] = t
		}
	}

	base, err := idx.LoadSyncState()
	if err != nil {
		return Report{}, false, fmt.Errorf("load sync state: %w", err)
	}

	syncedProjects := make(map[string]struct{}, len(cfg.ProjectVaults))
	for _, pv := range cfg.ProjectVaults {
		syncedProjects[pv.Project] = struct{}{}
	}

	// --- DECIDE ---
	plan := Decide(canon, proj, base, syncedProjects)

	// --- RENDER --- map plan per-project sets onto concrete files.
	canonByPath, projByPath := renderTargets(cfg, tasksDir, canonFiles, projFiles, plan)

	rep := Report{
		Files:     make(map[string]FileChange),
		Conflicts: plan.Conflicts,
		DryRun:    dryRun,
	}

	// Build the union of every file we read or want to write, so an emptied file
	// gets rewritten to empty too. Each entry pairs desired content with the
	// gather mtime for the TOCTOU guard.
	type writeTarget struct {
		path    string
		mtime   time.Time
		content []byte
		added   int
		removed int
		total   int
	}
	known := make(map[string]writeTarget)

	addTarget := func(path string, gatherMtime time.Time, prior []domain.Task, desired []domain.Task) error {
		content, total := renderFile(prior, desired)
		added, removed := lineDelta(prior, desired)
		known[path] = writeTarget{
			path:    path,
			mtime:   gatherMtime,
			content: content,
			added:   added,
			removed: removed,
			total:   total,
		}
		return nil
	}

	// Canon files: union of files read and projects with desired canon tasks.
	for path, fs := range canonFiles {
		desired := canonByPath[path]
		if err := addTarget(path, fs.mtime, fs.tasks, desired); err != nil {
			return Report{}, false, err
		}
	}
	for path, desired := range canonByPath {
		if _, seen := known[path]; seen {
			continue
		}
		// New canon file (project had no file yet). mtime zero -> guard expects absent.
		if err := addTarget(path, time.Time{}, nil, desired); err != nil {
			return Report{}, false, err
		}
	}

	// Projection files.
	for path, fs := range projFiles {
		desired := projByPath[path]
		if err := addTarget(path, fs.mtime, fs.tasks, desired); err != nil {
			return Report{}, false, err
		}
	}
	for path, desired := range projByPath {
		if _, seen := known[path]; seen {
			continue
		}
		if err := addTarget(path, time.Time{}, nil, desired); err != nil {
			return Report{}, false, err
		}
	}

	if dryRun {
		for path, wt := range known {
			if wt.added == 0 && wt.removed == 0 {
				continue
			}
			rep.Files[path] = FileChange{Path: path, Added: wt.added, Removed: wt.removed, Total: wt.total}
		}
		return rep, false, nil
	}

	// --- WRITE (TOCTOU-guarded) --- deterministic path order for stable retries.
	paths := make([]string, 0, len(known))
	for p := range known {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, p := range paths {
		wt := known[p]
		// Skip writing a file that is byte-identical to disk: avoids needless
		// mtime churn and keeps idempotency cheap.
		if !needsWrite(p, wt.content) {
			continue
		}
		if err := vault.WriteGuarded(p, wt.content, wt.mtime); err != nil {
			if err == vault.ErrConcurrentModification {
				// ABORT the whole pass; report the racing file and retry from gather.
				// Base is intentionally NOT committed -> next pass self-heals.
				return Report{Files: rep.Files, Conflicts: rep.Conflicts, Skipped: []string{p}, DryRun: false}, true, nil
			}
			return Report{}, false, fmt.Errorf("write %s: %w", p, err)
		}
		rep.Files[p] = FileChange{Path: p, Added: wt.added, Removed: wt.removed, Total: wt.total}
	}

	// --- COMMIT BASE LAST --- only after every file write succeeded.
	if err := idx.CommitSyncState(plan.BaseUpserts, plan.BaseDeletes); err != nil {
		return Report{}, false, fmt.Errorf("commit sync state: %w", err)
	}

	return rep, false, nil
}

// forceProject sets t.Project to project, ensuring project is its first tag so a
// re-format routes it to the right vault. ID-less projection lines that lacked
// the project tag (the projection file is named for the project, lines need not
// repeat it) thus become owned by that vault.
func forceProject(t domain.Task, project string) domain.Task {
	t.Project = project
	// Ensure the project tag is present and first.
	tags := make([]string, 0, len(t.Tags)+1)
	tags = append(tags, project)
	for _, tg := range t.Tags {
		if tg == project {
			continue
		}
		tags = append(tags, tg)
	}
	t.Tags = tags
	return t
}

// gatherCanonFiles reads every *.md in tasksDir (skipping sync-conflict copies),
// capturing each file's mtime and parsed tasks.
func gatherCanonFiles(tasksDir string) (map[string]fileState, error) {
	out := make(map[string]fileState)
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, fmt.Errorf("read tasks dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".md") || isSyncConflict(name) {
			continue
		}
		path := filepath.Join(tasksDir, name)
		fs, err := readFileState(path)
		if err != nil {
			return nil, err
		}
		out[path] = fs
	}
	return out, nil
}

// gatherProjectionFiles reads each configured project vault's projection file.
func gatherProjectionFiles(vaults []config.ProjectVault) (map[string]fileState, error) {
	out := make(map[string]fileState)
	for _, pv := range vaults {
		if isSyncConflict(filepath.Base(pv.File)) {
			continue
		}
		fs, err := readFileState(pv.File)
		if err != nil {
			return nil, err
		}
		out[pv.File] = fs
	}
	return out, nil
}

// readFileState captures the mtime then the parsed tasks of a file. mtime is
// read first so it reflects the on-disk state no later than the content read; a
// write that lands between the two only makes the guard stricter (false abort,
// then retry), never looser.
func readFileState(path string) (fileState, error) {
	mtime, err := vault.ReadFileMtime(path)
	if err != nil {
		return fileState{}, err
	}
	tasks, err := readTasksIfExists(path)
	if err != nil {
		return fileState{}, err
	}
	return fileState{path: path, mtime: mtime, tasks: tasks}, nil
}

// readTasksIfExists parses tasks from path, treating an absent file as empty.
func readTasksIfExists(path string) ([]domain.Task, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	tasks, err := vault.ReadTasks(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return tasks, nil
}

// renderTargets maps the plan's per-project task sets onto absolute file paths.
// Canon files: <tasksDir>/<flatten(project)>.md. Projection files: the
// configured project_vault file for that project.
func renderTargets(
	cfg config.Config,
	tasksDir string,
	canonFiles map[string]fileState,
	projFiles map[string]fileState,
	plan Plan,
) (canonByPath, projByPath map[string][]domain.Task) {
	canonByPath = make(map[string][]domain.Task)
	projByPath = make(map[string][]domain.Task)

	for project, tasks := range plan.CanonByProject {
		path := filepath.Join(tasksDir, flattenProject(project)+".md")
		canonByPath[path] = append(canonByPath[path], tasks...)
	}

	projFileFor := make(map[string]string, len(cfg.ProjectVaults))
	for _, pv := range cfg.ProjectVaults {
		projFileFor[pv.Project] = pv.File
	}
	for project, tasks := range plan.ProjByProject {
		path, ok := projFileFor[project]
		if !ok {
			continue
		}
		projByPath[path] = append(projByPath[path], tasks...)
	}
	return canonByPath, projByPath
}

// isSyncConflict reports whether a filename is an Obsidian Sync conflict copy,
// which must be skipped during all scans (mirrors service.isSyncConflict).
func isSyncConflict(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "sync-conflict") || strings.Contains(lower, "conflicted copy")
}

// flattenProject mirrors the main-vault filename rule: "/" -> "-".
func flattenProject(project string) string {
	if project == "" {
		return "inbox"
	}
	return strings.ReplaceAll(project, "/", "-")
}
