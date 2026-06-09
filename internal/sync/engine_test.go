package sync

import (
	"sort"
	"strings"
	"testing"

	"qi/internal/domain"
	"qi/internal/index"
)

func task(id, text, project string, completed bool) domain.Task {
	return domain.Task{ID: id, Text: text, Project: project, Tags: []string{project}, Completed: completed}
}

func canonOf(t domain.Task) string { return canonicalize(t) }

func synced(projects ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(projects))
	for _, p := range projects {
		m[p] = struct{}{}
	}
	return m
}

// hasTask reports whether the per-project set contains a task whose canonical
// line equals want.
func hasTask(set []domain.Task, want string) bool {
	for _, t := range set {
		if canonicalize(t) == want {
			return true
		}
	}
	return false
}

func upsertIDs(p Plan) []string {
	ids := make([]string, 0, len(p.BaseUpserts))
	for _, u := range p.BaseUpserts {
		ids = append(ids, u.ID)
	}
	sort.Strings(ids)
	return ids
}

func TestDecide_EightCases(t *testing.T) {
	const proj = "foo"
	sp := synced(proj)

	tNew := task("qi-00000001", "alpha", proj, false)
	base1 := index.SyncBase{ID: "qi-00000001", Project: proj, BaseLine: canonOf(tNew)}

	tests := []struct {
		name        string
		canon       map[string]domain.Task
		proj        map[string]domain.Task
		base        map[string]index.SyncBase
		wantCanon   bool   // task present in canon set for proj
		wantProj    bool   // task present in projection set for proj
		wantUpsert  bool   // base upsert for id
		wantDelete  bool   // base delete for id
		wantConflic bool   // conflict recorded
		id          string // id under test
	}{
		{
			name:       "case1 C1P0B0 new on main",
			canon:      map[string]domain.Task{tNew.ID: tNew},
			wantCanon:  true,
			wantProj:   true,
			wantUpsert: true,
			id:         tNew.ID,
		},
		{
			name:       "case2 C0P1B0 new from project",
			proj:       map[string]domain.Task{tNew.ID: tNew},
			wantCanon:  true,
			wantProj:   true,
			wantUpsert: true,
			id:         tNew.ID,
		},
		{
			name:       "case3 C1P1B0 equal -> seed base",
			canon:      map[string]domain.Task{tNew.ID: tNew},
			proj:       map[string]domain.Task{tNew.ID: tNew},
			wantCanon:  true,
			wantProj:   true,
			wantUpsert: true,
			id:         tNew.ID,
		},
		{
			name:        "case3 C1P1B0 unequal -> conflict",
			canon:       map[string]domain.Task{tNew.ID: tNew},
			proj:        map[string]domain.Task{tNew.ID: task("qi-00000001", "alpha EDIT", proj, false)},
			wantConflic: true,
			id:          tNew.ID,
		},
		{
			name:       "case4 C0P0B1 deleted both -> drop base",
			base:       map[string]index.SyncBase{base1.ID: base1},
			wantDelete: true,
			id:         tNew.ID,
		},
		{
			name:       "case5 C1P0B1 deleted in projection -> delete canon",
			canon:      map[string]domain.Task{tNew.ID: tNew},
			base:       map[string]index.SyncBase{base1.ID: base1},
			wantCanon:  false,
			wantDelete: true,
			id:         tNew.ID,
		},
		{
			name:       "case6 C0P1B1 deleted in main -> delete projection",
			proj:       map[string]domain.Task{tNew.ID: tNew},
			base:       map[string]index.SyncBase{base1.ID: base1},
			wantProj:   false,
			wantDelete: true,
			id:         tNew.ID,
		},
		{
			name:      "case7 3way mine==theirs==base -> noop",
			canon:     map[string]domain.Task{tNew.ID: tNew},
			proj:      map[string]domain.Task{tNew.ID: tNew},
			base:      map[string]index.SyncBase{base1.ID: base1},
			wantCanon: true,
			wantProj:  true,
			id:        tNew.ID,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan := Decide(tc.canon, tc.proj, tc.base, sp)
			want := canonOf(tNew)

			gotCanon := hasTask(plan.CanonByProject[proj], want)
			if tc.wantCanon && !gotCanon {
				t.Errorf("expected task in canon set, got %v", plan.CanonByProject[proj])
			}
			if tc.name == "case5 C1P0B1 deleted in projection -> delete canon" && gotCanon {
				t.Errorf("did not expect task in canon set after deletion")
			}

			gotProj := hasTask(plan.ProjByProject[proj], want)
			if tc.wantProj && !gotProj {
				t.Errorf("expected task in projection set, got %v", plan.ProjByProject[proj])
			}

			gotUpsert := false
			for _, u := range plan.BaseUpserts {
				if u.ID == tc.id {
					gotUpsert = true
				}
			}
			if tc.wantUpsert && !gotUpsert {
				t.Errorf("expected base upsert for %s, upserts=%v", tc.id, upsertIDs(plan))
			}

			gotDelete := false
			for _, d := range plan.BaseDeletes {
				if d == tc.id {
					gotDelete = true
				}
			}
			if tc.wantDelete != gotDelete {
				t.Errorf("base delete for %s = %v, want %v", tc.id, gotDelete, tc.wantDelete)
			}

			gotConflict := len(plan.Conflicts) > 0
			if tc.wantConflic != gotConflict {
				t.Errorf("conflict = %v, want %v", gotConflict, tc.wantConflic)
			}
		})
	}
}

func TestDecide_3way_OneSideChanged(t *testing.T) {
	const proj = "foo"
	sp := synced(proj)
	id := "qi-aaaaaaaa"

	baseTask := task(id, "alpha", proj, false)
	base := map[string]index.SyncBase{id: {ID: id, Project: proj, BaseLine: canonOf(baseTask)}}

	// projection changed (completed), main unchanged -> canon := theirs.
	t.Run("only projection changed", func(t *testing.T) {
		theirs := task(id, "alpha", proj, true)
		plan := Decide(
			map[string]domain.Task{id: baseTask},
			map[string]domain.Task{id: theirs},
			base, sp,
		)
		if !hasTask(plan.CanonByProject[proj], canonOf(theirs)) {
			t.Errorf("canon should adopt projection's completed line")
		}
		if len(plan.Conflicts) != 0 {
			t.Errorf("unexpected conflict")
		}
	})

	// main changed, projection unchanged -> projection := mine.
	t.Run("only main changed", func(t *testing.T) {
		mine := task(id, "alpha v2", proj, false)
		plan := Decide(
			map[string]domain.Task{id: mine},
			map[string]domain.Task{id: baseTask},
			base, sp,
		)
		if !hasTask(plan.ProjByProject[proj], canonOf(mine)) {
			t.Errorf("projection should adopt main's edited line")
		}
		if len(plan.Conflicts) != 0 {
			t.Errorf("unexpected conflict")
		}
	})

	// both changed differently -> conflict, base untouched.
	t.Run("both changed -> conflict", func(t *testing.T) {
		mine := task(id, "alpha main", proj, false)
		theirs := task(id, "alpha proj", proj, false)
		plan := Decide(
			map[string]domain.Task{id: mine},
			map[string]domain.Task{id: theirs},
			base, sp,
		)
		if len(plan.Conflicts) != 1 {
			t.Fatalf("expected 1 conflict, got %d", len(plan.Conflicts))
		}
		for _, u := range plan.BaseUpserts {
			if u.ID == id {
				t.Errorf("base must NOT be upserted for a conflicting id")
			}
		}
		// keep-both: original + a tagged copy in the canon set.
		set := plan.CanonByProject[proj]
		var tagged int
		for _, tk := range set {
			if strings.Contains(canonicalize(tk), "#"+SyncConflictTag) {
				tagged++
			}
		}
		if tagged != 1 {
			t.Errorf("expected exactly one #%s line, got %d in %v", SyncConflictTag, tagged, set)
		}
		if len(set) != 2 {
			t.Errorf("expected keep-both (2 lines), got %d", len(set))
		}
	})
}

func TestDecide_Reassignment(t *testing.T) {
	// foo -> bar: task's first tag is now bar; base recorded foo.
	sp := synced("foo", "bar")
	id := "qi-deadbeef"

	moved := domain.Task{ID: id, Text: "ship it", Project: "bar", Tags: []string{"bar"}}
	base := map[string]index.SyncBase{id: {ID: id, Project: "foo", BaseLine: canonicalize(
		domain.Task{ID: id, Text: "ship it", Project: "foo", Tags: []string{"foo"}},
	)}}

	plan := Decide(
		map[string]domain.Task{id: moved},
		map[string]domain.Task{}, // projection of foo no longer relevant; bar empty
		base, sp,
	)

	// Lands in bar's canon + bar's projection.
	if !hasTask(plan.CanonByProject["bar"], canonicalize(moved)) {
		t.Errorf("moved task should be in bar canon, got %v", plan.CanonByProject["bar"])
	}
	if !hasTask(plan.ProjByProject["bar"], canonicalize(moved)) {
		t.Errorf("moved task should be in bar projection, got %v", plan.ProjByProject["bar"])
	}
	// Gone from foo (no entries for foo at all).
	if len(plan.CanonByProject["foo"]) != 0 {
		t.Errorf("foo canon should be empty, got %v", plan.CanonByProject["foo"])
	}
	if len(plan.ProjByProject["foo"]) != 0 {
		t.Errorf("foo projection should be empty, got %v", plan.ProjByProject["foo"])
	}
	// Base updated to bar; NOT deleted (no spurious delete-from-canon).
	var upsertedToBar bool
	for _, u := range plan.BaseUpserts {
		if u.ID == id {
			if u.Project != "bar" {
				t.Errorf("base project should be bar, got %s", u.Project)
			}
			upsertedToBar = true
		}
	}
	if !upsertedToBar {
		t.Errorf("expected base upsert to bar for moved task")
	}
	for _, d := range plan.BaseDeletes {
		if d == id {
			t.Errorf("moved task must NOT be base-deleted (would lose data)")
		}
	}
	if len(plan.Conflicts) != 0 {
		t.Errorf("reassignment must not be a conflict, got %v", plan.Conflicts)
	}
}

func TestDecide_UnsyncedProjectNeverProjected(t *testing.T) {
	// foo is synced; baz is not configured.
	sp := synced("foo")
	bazTask := task("qi-11111111", "private", "baz", false)
	plan := Decide(
		map[string]domain.Task{bazTask.ID: bazTask},
		map[string]domain.Task{},
		map[string]index.SyncBase{},
		sp,
	)
	if !hasTask(plan.CanonByProject["baz"], canonicalize(bazTask)) {
		t.Errorf("unsynced task should stay in canon")
	}
	if len(plan.ProjByProject["baz"]) != 0 {
		t.Errorf("unsynced project must never be projected, got %v", plan.ProjByProject["baz"])
	}
	// CRITICAL: pass 1 must NOT seed base for an unsynced task. If it does, the
	// next pass sees canon-present/projection-absent/base-present and deletes
	// the task from canon -> silent data loss. (Regression: bhq/ODNF/Qi wipe.)
	for _, u := range plan.BaseUpserts {
		if u.ID == bazTask.ID {
			t.Fatalf("unsynced task must NOT be base-upserted (would trigger Case 5 delete next pass)")
		}
	}
}

// TestDecide_UnsyncedProjectSurvivesSecondPass reproduces the data-loss bug
// directly: an unsynced canon task with a STALE base row (as a pre-fix pass
// would have seeded) must be KEPT in canon, never deleted, and the stale base
// row dropped.
func TestDecide_UnsyncedProjectSurvivesSecondPass(t *testing.T) {
	sp := synced("foo")
	bazTask := task("qi-22222222", "keep me", "baz", false)
	staleBase := map[string]index.SyncBase{
		bazTask.ID: {ID: bazTask.ID, Project: "baz", BaseLine: canonOf(bazTask)},
	}
	plan := Decide(
		map[string]domain.Task{bazTask.ID: bazTask},
		map[string]domain.Task{},
		staleBase,
		sp,
	)
	if !hasTask(plan.CanonByProject["baz"], canonicalize(bazTask)) {
		t.Fatalf("unsynced task must survive a second pass, got canon=%v", plan.CanonByProject["baz"])
	}
	var dropped bool
	for _, d := range plan.BaseDeletes {
		if d == bazTask.ID {
			dropped = true
		}
	}
	if !dropped {
		t.Errorf("stale base row for unsynced project should be dropped")
	}
}
