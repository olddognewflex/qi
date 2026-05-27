package sync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qi/internal/config"
	"qi/internal/index"
	"qi/internal/vault"
)

// harness builds a main vault with a 10-tasks dir and one project vault "foo".
type harness struct {
	t         *testing.T
	cfg       config.Config
	idx       *index.Indexer
	tasksDir  string
	projFile  string // foo projection file abs path
	canonFoo  string // main vault foo canon file abs path
}

func newHarness(t *testing.T, projects ...string) *harness {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	mainVault := t.TempDir()
	tasksDir := filepath.Join(mainVault, "10-tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if len(projects) == 0 {
		projects = []string{"foo"}
	}
	var pvs []config.ProjectVault
	for _, p := range projects {
		vaultDir := t.TempDir()
		pf := filepath.Join(vaultDir, "10-tasks", p+".md")
		if err := os.MkdirAll(filepath.Dir(pf), 0o755); err != nil {
			t.Fatal(err)
		}
		pvs = append(pvs, config.ProjectVault{Project: p, Path: vaultDir, File: pf})
	}

	cfg := config.Config{
		VaultPath:     mainVault,
		TaskFilePath:  filepath.Join(tasksDir, "inbox.md"),
		ProjectVaults: pvs,
	}

	idx, err := index.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Close() })

	return &harness{
		t:        t,
		cfg:      cfg,
		idx:      idx,
		tasksDir: tasksDir,
		projFile: pvs[0].File,
		canonFoo: filepath.Join(tasksDir, "foo.md"),
	}
}

func (h *harness) projFileFor(project string) string {
	for _, pv := range h.cfg.ProjectVaults {
		if pv.Project == project {
			return pv.File
		}
	}
	h.t.Fatalf("no project vault for %q", project)
	return ""
}

func (h *harness) canonFileFor(project string) string {
	return filepath.Join(h.tasksDir, flattenProject(project)+".md")
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatal(err)
	}
	return string(b)
}

func (h *harness) reconcile() Report {
	h.t.Helper()
	rep, err := Reconcile(h.cfg, h.idx, false)
	if err != nil {
		h.t.Fatalf("reconcile: %v", err)
	}
	return rep
}

func TestReconcile_NewOnMain_ReachesProjection(t *testing.T) {
	h := newHarness(t)
	writeFile(t, h.canonFoo, "- [ ] alpha #foo ^qi-00000001\n")

	h.reconcile()

	got := readFile(t, h.projFile)
	if !strings.Contains(got, "qi-00000001") || !strings.Contains(got, "alpha") {
		t.Errorf("projection missing new task: %q", got)
	}
}

func TestReconcile_NewFromProject_MintedAndIngested(t *testing.T) {
	h := newHarness(t)
	// id-less line in projection -> mint + ingest to canon.
	writeFile(t, h.projFile, "- [ ] from phone\n")

	h.reconcile()

	canon := readFile(t, h.canonFoo)
	if !strings.Contains(canon, "from phone") {
		t.Errorf("canon missing minted task: %q", canon)
	}
	if !strings.Contains(canon, "^qi-") {
		t.Errorf("minted task should carry a qi id: %q", canon)
	}
	// projection now also carries the id.
	proj := readFile(t, h.projFile)
	if !strings.Contains(proj, "^qi-") {
		t.Errorf("projection should be rewritten with id: %q", proj)
	}
}

func TestReconcile_CompleteFlowsAcross(t *testing.T) {
	h := newHarness(t)
	writeFile(t, h.canonFoo, "- [ ] alpha #foo ^qi-00000001\n")
	h.reconcile() // establish base + projection

	// complete on the PROJECTION side.
	writeFile(t, h.projFile, "- [x] alpha #foo ^qi-00000001\n")
	h.reconcile()

	canon := readFile(t, h.canonFoo)
	if !strings.Contains(canon, "- [x]") {
		t.Errorf("completion should flow main<-projection: %q", canon)
	}
}

func TestReconcile_DeleteOnEitherSide(t *testing.T) {
	h := newHarness(t)
	writeFile(t, h.canonFoo, "- [ ] alpha #foo ^qi-00000001\n- [ ] beta #foo ^qi-00000002\n")
	h.reconcile()

	// delete beta from projection.
	writeFile(t, h.projFile, "- [ ] alpha #foo ^qi-00000001\n")
	h.reconcile()

	canon := readFile(t, h.canonFoo)
	if strings.Contains(canon, "qi-00000002") {
		t.Errorf("deleted-in-projection task should be removed from canon: %q", canon)
	}
	if !strings.Contains(canon, "qi-00000001") {
		t.Errorf("surviving task lost: %q", canon)
	}
}

func TestReconcile_Reassignment_FooToBar(t *testing.T) {
	h := newHarness(t, "foo", "bar")
	id := "^qi-aaaaaaaa"
	writeFile(t, h.canonFileFor("foo"), "- [ ] move me #foo "+id+"\n")
	h.reconcile() // foo canon + foo projection + base{foo}

	// reassign: change tag foo -> bar in the main canon file.
	// (Simulate the user editing the tag; task moves to bar's canon file via
	//  the global pass. Here we write the moved line into bar's canon file and
	//  remove from foo's, as `qi task` routing would, then let sync converge.)
	// Realistically the edit happens in-place; emulate by moving the file line.
	writeFile(t, h.canonFileFor("foo"), "")
	writeFile(t, h.canonFileFor("bar"), "- [ ] move me #bar "+id+"\n")
	h.reconcile()

	bar := readFile(t, h.canonFileFor("bar"))
	if !strings.Contains(bar, "qi-aaaaaaaa") || !strings.Contains(bar, "#bar") {
		t.Errorf("moved task should be in bar canon: %q", bar)
	}
	barProj := readFile(t, h.projFileFor("bar"))
	if !strings.Contains(barProj, "qi-aaaaaaaa") {
		t.Errorf("moved task should be in bar projection: %q", barProj)
	}
	fooProj := readFile(t, h.projFileFor("foo"))
	if strings.Contains(fooProj, "qi-aaaaaaaa") {
		t.Errorf("moved task should be GONE from foo projection: %q", fooProj)
	}
	fooCanon := readFile(t, h.canonFileFor("foo"))
	if strings.Contains(fooCanon, "qi-aaaaaaaa") {
		t.Errorf("moved task should be GONE from foo canon: %q", fooCanon)
	}

	// base now records bar; no spurious delete left it dangling.
	base, _ := h.idx.LoadSyncState()
	if b, ok := base["qi-aaaaaaaa"]; !ok || b.Project != "bar" {
		t.Errorf("base should record project=bar, got %+v ok=%v", b, ok)
	}
}

func TestReconcile_TrueConflict_KeepBothBaseUntouched(t *testing.T) {
	h := newHarness(t)
	writeFile(t, h.canonFoo, "- [ ] alpha #foo ^qi-00000001\n")
	h.reconcile()
	baseBefore, _ := h.idx.LoadSyncState()
	wantBaseLine := baseBefore["qi-00000001"].BaseLine

	// edit BOTH sides differently.
	writeFile(t, h.canonFoo, "- [ ] alpha MAIN #foo ^qi-00000001\n")
	writeFile(t, h.projFile, "- [ ] alpha PROJ #foo ^qi-00000001\n")
	rep := h.reconcile()

	if len(rep.Conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(rep.Conflicts))
	}
	canon := readFile(t, h.canonFoo)
	if !strings.Contains(canon, "alpha MAIN") || !strings.Contains(canon, "alpha PROJ") {
		t.Errorf("conflict should keep BOTH lines: %q", canon)
	}
	if !strings.Contains(canon, "#"+SyncConflictTag) {
		t.Errorf("one line should be tagged #%s: %q", SyncConflictTag, canon)
	}

	baseAfter, _ := h.idx.LoadSyncState()
	if baseAfter["qi-00000001"].BaseLine != wantBaseLine {
		t.Errorf("base must be UNTOUCHED on conflict: was %q now %q", wantBaseLine, baseAfter["qi-00000001"].BaseLine)
	}
}

func TestReconcile_Idempotency(t *testing.T) {
	h := newHarness(t)
	writeFile(t, h.canonFoo, "- [ ] alpha #foo ^qi-00000001\n- [ ] beta #foo ^qi-00000002\n")
	h.reconcile()

	canon1 := readFile(t, h.canonFoo)
	proj1 := readFile(t, h.projFile)

	rep := h.reconcile()
	if len(rep.Files) != 0 {
		t.Errorf("second run should write nothing, wrote: %v", rep.Files)
	}
	if got := readFile(t, h.canonFoo); got != canon1 {
		t.Errorf("canon not byte-identical on rerun:\n%q\nvs\n%q", canon1, got)
	}
	if got := readFile(t, h.projFile); got != proj1 {
		t.Errorf("projection not byte-identical on rerun:\n%q\nvs\n%q", proj1, got)
	}
}

func TestReconcile_PartialWriteRecovery_NoFalseConflict(t *testing.T) {
	h := newHarness(t)
	writeFile(t, h.canonFoo, "- [ ] alpha #foo ^qi-00000001\n")
	h.reconcile()

	// Simulate a crash: main got a new edit and the canon file was written, but
	// base was NOT committed for the change (stale base = pre-edit line).
	// Emulate by editing main and clearing base for that id.
	writeFile(t, h.canonFoo, "- [ ] alpha v2 #foo ^qi-00000001\n")
	// roll base back to the original line to mimic "base never committed".
	if err := h.idx.CommitSyncState([]index.SyncBase{{
		ID: "qi-00000001", Project: "foo",
		BaseLine: "- [ ] alpha #foo ^qi-00000001",
	}}, nil); err != nil {
		t.Fatal(err)
	}

	// Rerun: base==theirs(projection still old) and base!=mine(main edited) ->
	// "only main changed" -> projection adopts main. NOT a conflict.
	rep := h.reconcile()
	if len(rep.Conflicts) != 0 {
		t.Fatalf("partial-write recovery must NOT raise a false conflict, got %v", rep.Conflicts)
	}
	proj := readFile(t, h.projFile)
	if !strings.Contains(proj, "alpha v2") {
		t.Errorf("projection should converge to main edit: %q", proj)
	}
}

func TestReconcile_TOCTOU_AbortOnMtimeBump(t *testing.T) {
	h := newHarness(t)
	writeFile(t, h.canonFoo, "- [ ] alpha #foo ^qi-00000001\n")

	// First establish a clean state.
	h.reconcile()

	// Now stage a change on main and, mid-sync, bump the projection file's mtime
	// to simulate an Obsidian Sync write landing between gather and write.
	// We exercise WriteGuarded directly to prove the abort path: capture mtime,
	// then bump it, then guarded write must return ErrConcurrentModification.
	mtime, err := vault.ReadFileMtime(h.projFile)
	if err != nil {
		t.Fatal(err)
	}
	// Bump mtime forward.
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(h.projFile, future, future); err != nil {
		t.Fatal(err)
	}
	err = vault.WriteGuarded(h.projFile, []byte("- [ ] x ^qi-00000009\n"), mtime)
	if err != vault.ErrConcurrentModification {
		t.Fatalf("expected ErrConcurrentModification, got %v", err)
	}
	// The guarded write must NOT have modified the file.
	if strings.Contains(readFile(t, h.projFile), "qi-00000009") {
		t.Errorf("guarded write should have been refused")
	}
}
