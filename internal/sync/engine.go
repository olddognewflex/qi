// Package sync implements qi's global bidirectional task reconcile between the
// main vault's per-project task files and each configured project vault's
// projection file, using the SQLite task_sync_state table as the 3-way merge
// ancestor.
//
// The package splits into two layers:
//   - a PURE decision function (Decide) that takes in-memory task maps and the
//     base ancestor and returns a Plan, with no file or DB access; and
//   - an I/O orchestration function (Reconcile) that gathers state from disk,
//     calls Decide, renders deterministic files, writes them under a TOCTOU
//     guard, and commits the base last.
//
// All equality comparisons operate on the canonical vault.FormatTaskLine output
// (the canonicalize helper), never raw file lines, so whitespace and key-order
// reformatting are invisible and a checkbox flip is a real change.
package sync

import (
	"sort"

	"qi/internal/domain"
	"qi/internal/index"
	"qi/internal/vault"
)

// SyncConflictTag is appended to one side of a true conflict so the divergence
// is visible in the vault and the user can resolve it manually.
const SyncConflictTag = "sync-conflict"

// Plan is the pure decision output. It is expressed per-project (not per-file);
// the orchestrator maps each project to its canon file and projection file.
//
// CanonByProject and ProjByProject hold the FULL desired task set for each
// project's canon file / projection file respectively — qi owns these files, so
// the renderer rewrites them wholesale from these sets. A project key present in
// either map (even with an empty slice) means that file should be (re)written.
type Plan struct {
	// CanonByProject: project -> desired tasks in the main-vault canon file.
	CanonByProject map[string][]domain.Task
	// ProjByProject: project -> desired tasks in that vault's projection file.
	// Only synced projects appear here.
	ProjByProject map[string][]domain.Task

	// BaseUpserts / BaseDeletes mutate task_sync_state. Committed LAST by the
	// orchestrator, after every file write succeeds.
	BaseUpserts []index.SyncBase
	BaseDeletes []string

	// Conflicts records ids that diverged on both sides. The conflicting task
	// lines are still placed in the relevant file sets (keep-both); base is left
	// untouched for these ids.
	Conflicts []Conflict
}

// Conflict describes one id whose canon and projection both diverged from base.
type Conflict struct {
	ID      string
	Project string
}

// canonicalize returns the canonical FormatTaskLine output for a task, used for
// every equality test. A task that fails to format (empty text) yields "" which
// only ever compares equal to another empty — acceptable since such a task can
// never be written anyway.
func canonicalize(t domain.Task) string {
	line, err := vault.FormatTaskLine(t)
	if err != nil {
		return ""
	}
	return line
}

// tagged returns a copy of t with the sync-conflict tag appended (if absent).
func tagged(t domain.Task) domain.Task {
	for _, tg := range t.Tags {
		if tg == SyncConflictTag {
			return t
		}
	}
	cp := t
	cp.Tags = append(append([]string{}, t.Tags...), SyncConflictTag)
	return cp
}

// Decide is the PURE 3-way merge. Inputs:
//
//   - canon: id -> task from main-vault per-project files. task.Project is the
//     task's first tag (its current owning project).
//   - proj:  id -> task from project-vault projection files. task.Project has
//     already been forced to the owning vault's project by the caller, and any
//     id-less projection line has already been assigned a freshly minted id.
//   - base:  id -> ancestor {project, baseLine} from the last sync.
//   - syncedProjects: the set of projects that have a configured project.
//     Only these are ever projected; tasks of other projects stay in canon.
//
// It returns a Plan with the full desired task set for every touched project's
// canon and projection files, plus base upserts/deletes and conflicts.
func Decide(
	canon map[string]domain.Task,
	proj map[string]domain.Task,
	base map[string]index.SyncBase,
	syncedProjects map[string]struct{},
) Plan {
	plan := Plan{
		CanonByProject: make(map[string][]domain.Task),
		ProjByProject:  make(map[string][]domain.Task),
	}

	// Collect every id across the three maps, sorted for deterministic output.
	idset := make(map[string]struct{})
	for id := range canon {
		idset[id] = struct{}{}
	}
	for id := range proj {
		idset[id] = struct{}{}
	}
	for id := range base {
		idset[id] = struct{}{}
	}
	ids := make([]string, 0, len(idset))
	for id := range idset {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	isSynced := func(project string) bool {
		_, ok := syncedProjects[project]
		return ok
	}

	// putCanon / putProj append a winning task to the desired set for its
	// project. putProj only emits when the project is synced.
	putCanon := func(t domain.Task) {
		plan.CanonByProject[t.Project] = append(plan.CanonByProject[t.Project], t)
	}
	putProj := func(t domain.Task) {
		if isSynced(t.Project) {
			plan.ProjByProject[t.Project] = append(plan.ProjByProject[t.Project], t)
		}
	}
	upsertBase := func(t domain.Task) {
		plan.BaseUpserts = append(plan.BaseUpserts, index.SyncBase{
			ID:       t.ID,
			Project:  t.Project,
			BaseLine: canonicalize(t),
		})
	}

	for _, id := range ids {
		c, hasC := canon[id]
		p, hasP := proj[id]
		b, hasB := base[id]

		// REASSIGNMENT: the task exists in canon and base, and its current
		// project differs from the project recorded at last sync -> it MOVED.
		// We must NOT let the id-keyed base (which belongs to the OLD project)
		// drive a spurious "deleted in projection -> delete from canon" branch.
		// Treat it as a fresh placement in the NEW project, exactly as if no
		// base existed for it: route to new canon + new projection, and update
		// base to the new project. The old project's files simply stop
		// containing this id (we never emit it there), so Sync deletes it from
		// the old projection.
		if hasC && hasB && c.Project != b.Project {
			putCanon(c)
			putProj(c)
			upsertBase(c)
			continue
		}

		switch {
		case hasC && !hasP && !hasB:
			// Case 1: NEW on main -> project its line; seed base.
			putCanon(c)
			putProj(c)
			upsertBase(c)

		case !hasC && hasP && !hasB:
			// Case 2: NEW from project (post-mint) -> write to canon; seed base.
			putCanon(p)
			putProj(p)
			upsertBase(p)

		case hasC && hasP && !hasB:
			// Case 3: exists both, no base.
			if canonicalize(c) == canonicalize(p) {
				putCanon(c)
				putProj(c)
				upsertBase(c)
			} else {
				// CONFLICT: keep both, tag one, do NOT seed base.
				putCanon(c)
				putCanon(tagged(p))
				putProj(c)
				putProj(tagged(p))
				plan.Conflicts = append(plan.Conflicts, Conflict{ID: id, Project: c.Project})
			}

		case !hasC && !hasP && hasB:
			// Case 4: deleted both sides -> drop base row.
			plan.BaseDeletes = append(plan.BaseDeletes, id)

		case hasC && !hasP && hasB:
			// Case 5: deleted in projection -> delete from canon; drop base.
			// (Not a reassignment: c.Project == b.Project here.)
			plan.BaseDeletes = append(plan.BaseDeletes, id)

		case !hasC && hasP && hasB:
			// Case 6: deleted in main -> delete from projection; drop base.
			plan.BaseDeletes = append(plan.BaseDeletes, id)

		case hasC && hasP && hasB:
			// Case 7: full 3-way merge on canonical form.
			mine := canonicalize(c)   // main side
			theirs := canonicalize(p) // projection side
			baseLine := b.BaseLine
			switch {
			case mine == theirs:
				putCanon(c)
				putProj(c)
				if mine != baseLine {
					upsertBase(c)
				}
				// else: identical to base -> no base change needed, but the
				// upsert is harmless; we skip it to keep the plan minimal.
			case baseLine == mine:
				// only projection changed -> canon := theirs.
				putCanon(p)
				putProj(p)
				upsertBase(p)
			case baseLine == theirs:
				// only main changed -> projection := mine.
				putCanon(c)
				putProj(c)
				upsertBase(c)
			default:
				// all three differ -> CONFLICT: keep both, tag one, base untouched.
				putCanon(c)
				putCanon(tagged(p))
				putProj(c)
				putProj(tagged(p))
				plan.Conflicts = append(plan.Conflicts, Conflict{ID: id, Project: c.Project})
			}

		default:
			// !hasC && !hasP && !hasB is impossible (id came from a map).
		}
	}

	return plan
}
