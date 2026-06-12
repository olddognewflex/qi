package service

import (
	"testing"

	"qi/internal/calendar"
	"qi/internal/domain"
)

func TestPlanBlocks_SequentialPlacement(t *testing.T) {
	in := PlanInput{
		Tasks:    []domain.Task{{Text: "a"}, {Text: "b"}, {Text: "c"}},
		StartMin: 540, // 09:00
		BlockMin: 30,
		Limit:    6,
	}
	got := PlanBlocks(in)
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3", len(got))
	}
	wantStarts := []int{540, 570, 600} // 09:00, 09:30, 10:00
	for i, w := range wantStarts {
		if got[i].StartMin != w {
			t.Errorf("entry %d start = %d, want %d", i, got[i].StartMin, w)
		}
	}
}

func TestPlanBlocks_ExistingEventBlocksSlot(t *testing.T) {
	in := PlanInput{
		Tasks:    []domain.Task{{Text: "first"}},
		Existing: []calendar.ScheduleEntry{{StartMin: 540, EndMin: 600, Title: "Meeting"}},
		StartMin: 540,
		BlockMin: 30,
		Limit:    6,
	}
	got := PlanBlocks(in)
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].StartMin != 600 { // pushed past the 09:00-10:00 event
		t.Errorf("start = %d, want 600", got[0].StartMin)
	}
}

func TestPlanBlocks_DedupSkipsScheduledTitle(t *testing.T) {
	in := PlanInput{
		Tasks:    []domain.Task{{Text: "Ship release"}, {Text: "Write docs"}},
		Existing: []calendar.ScheduleEntry{{StartMin: 540, EndMin: 600, Title: "ship release"}},
		StartMin: 600,
		BlockMin: 30,
		Limit:    6,
	}
	got := PlanBlocks(in)
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1 (dedup): %+v", len(got), got)
	}
	if got[0].Title != "Write docs" {
		t.Errorf("title = %q, want %q", got[0].Title, "Write docs")
	}
}

func TestPlanBlocks_LimitCaps(t *testing.T) {
	in := PlanInput{
		Tasks:    []domain.Task{{Text: "a"}, {Text: "b"}, {Text: "c"}, {Text: "d"}},
		StartMin: 540,
		BlockMin: 30,
		Limit:    2,
	}
	got := PlanBlocks(in)
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
}

func TestPlanBlocks_EmptyTasks(t *testing.T) {
	got := PlanBlocks(PlanInput{StartMin: 540, BlockMin: 30, Limit: 6})
	if len(got) != 0 {
		t.Fatalf("got %d entries, want 0", len(got))
	}
}

func TestPlanBlocks_TitleProjectStripped(t *testing.T) {
	in := PlanInput{
		Tasks:    []domain.Task{{Text: "Buy milk #home", Project: "home"}},
		StartMin: 540,
		BlockMin: 30,
		Limit:    6,
	}
	got := PlanBlocks(in)
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	e := got[0]
	if e.Title != "Buy milk" || e.Project != "home" {
		t.Errorf("got title=%q project=%q, want %q/%q", e.Title, e.Project, "Buy milk", "home")
	}
	if got := e.Render(); got != "- 09:00-09:30 Buy milk #home" {
		t.Errorf("Render = %q (project duplicated?)", got)
	}
}

func TestMergeAndRender_SortsByTime(t *testing.T) {
	existing := []calendar.ScheduleEntry{{StartMin: 600, EndMin: 660, Title: "Late"}}
	planned := []calendar.ScheduleEntry{{StartMin: 540, EndMin: 570, Title: "Early"}}
	got := MergeAndRender(existing, planned)
	want := "- 09:00-09:30 Early\n- 10:00-11:00 Late"
	if got != want {
		t.Errorf("MergeAndRender =\n%q\nwant\n%q", got, want)
	}
}
