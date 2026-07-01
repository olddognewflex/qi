package commands

import (
	"testing"
	"time"

	"qi/internal/domain"
)

func TestParseScheduleDate(t *testing.T) {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)

	cases := []struct {
		in   string
		want time.Time
	}{
		{"2026-06-25", time.Date(2026, 6, 25, 0, 0, 0, 0, time.Local)},
		{"today", today},
		{"TODAY", today},
		{"tomorrow", today.AddDate(0, 0, 1)},
		{"+3d", today.AddDate(0, 0, 3)},
		{" +0d ", today},
	}
	for _, c := range cases {
		got, err := parseScheduleDate(c.in)
		if err != nil {
			t.Fatalf("parseScheduleDate(%q): %v", c.in, err)
		}
		if !got.Equal(c.want) {
			t.Errorf("parseScheduleDate(%q) = %v, want %v", c.in, got, c.want)
		}
	}

	for _, bad := range []string{"", "nope", "2026/06/25", "+xd", "june"} {
		if _, err := parseScheduleDate(bad); err == nil {
			t.Errorf("parseScheduleDate(%q) expected error, got nil", bad)
		}
	}
}

func TestScheduleSpecResolveRelative(t *testing.T) {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)

	spec, err := parseScheduleSpec("+3d")
	if err != nil {
		t.Fatalf("parseScheduleSpec: %v", err)
	}
	if !spec.relative {
		t.Fatalf("expected +3d to be a relative spec")
	}

	// Unscheduled task: offset from today.
	if got := spec.resolve(domain.Task{}); !got.Equal(today.AddDate(0, 0, 3)) {
		t.Errorf("resolve(unscheduled) = %v, want %v", got, today.AddDate(0, 0, 3))
	}

	// Scheduled task: offset from its scheduled date, not today.
	sched := time.Date(2026, 6, 25, 0, 0, 0, 0, time.Local)
	want := sched.AddDate(0, 0, 3)
	if got := spec.resolve(domain.Task{Scheduled: &sched}); !got.Equal(want) {
		t.Errorf("resolve(scheduled) = %v, want %v", got, want)
	}

	// Absolute specs ignore the task's scheduled date.
	abs, err := parseScheduleSpec("2026-06-25")
	if err != nil {
		t.Fatalf("parseScheduleSpec: %v", err)
	}
	if got := abs.resolve(domain.Task{Scheduled: &sched}); !got.Equal(sched) {
		t.Errorf("resolve(absolute) = %v, want %v", got, sched)
	}
}

func TestFilterByProject(t *testing.T) {
	tasks := []domain.Task{
		{Text: "a", Project: "qi"},
		{Text: "b", Project: "work"},
		{Text: "c", Project: "QI"},
		{Text: "d"},
	}
	got := filterByProject(tasks, "qi")
	if len(got) != 2 {
		t.Fatalf("want 2 qi tasks (case-insensitive), got %d", len(got))
	}
	if got := filterByProject(tasks, "none"); len(got) != 0 {
		t.Fatalf("want 0, got %d", len(got))
	}
}
