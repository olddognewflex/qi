package calendar

import "testing"

func TestParseScheduleEntries_Range(t *testing.T) {
	got := ParseScheduleEntries("- 09:00-09:30 Standup")
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	e := got[0]
	if e.StartMin != 540 || e.EndMin != 570 || e.Title != "Standup" || e.Project != "" {
		t.Errorf("got %+v", e)
	}
}

func TestParseScheduleEntries_SingleTimePlus60(t *testing.T) {
	got := ParseScheduleEntries("- 14:00 Deep work")
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	e := got[0]
	if e.StartMin != 840 || e.EndMin != 900 {
		t.Errorf("got start=%d end=%d, want 840/900", e.StartMin, e.EndMin)
	}
}

func TestParseScheduleEntries_Project(t *testing.T) {
	got := ParseScheduleEntries("- 09:00-10:00 Ship release #qi")
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	e := got[0]
	if e.Title != "Ship release" || e.Project != "qi" {
		t.Errorf("got title=%q project=%q", e.Title, e.Project)
	}
}

func TestParseScheduleEntries_SkipsNonMatching(t *testing.T) {
	body := "not a schedule line\n- 09:00-09:30 Standup\n- nope"
	got := ParseScheduleEntries(body)
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(got), got)
	}
}

func TestScheduleEntry_RenderMatchesRangeRe(t *testing.T) {
	e := ScheduleEntry{StartMin: 540, EndMin: 570, Title: "Standup", Project: "qi"}
	line := e.Render()
	want := "- 09:00-09:30 Standup #qi"
	if line != want {
		t.Errorf("Render = %q, want %q", line, want)
	}
	if eventRangeRe.FindStringSubmatch(line) == nil {
		t.Errorf("rendered line %q does not match eventRangeRe", line)
	}
}

func TestScheduleEntry_RoundTrip(t *testing.T) {
	in := ScheduleEntry{StartMin: 615, EndMin: 645, Title: "Email triage", Project: "ops"}
	got := ParseScheduleEntries(in.Render())
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	e := got[0]
	if e.StartMin != in.StartMin || e.EndMin != in.EndMin || e.Title != in.Title || e.Project != in.Project {
		t.Errorf("round-trip: got %+v, want %+v", e, in)
	}
}
