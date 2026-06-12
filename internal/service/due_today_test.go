package service

import (
	"testing"
	"time"

	"qi/internal/domain"
)

// TestFilterForDay_TimezoneSkew guards the regression that `qi plan` exposed:
// task dates are parsed at UTC midnight (time.Parse("2006-01-02")), while the
// target day comes from time.Now() in Local. Converting one into the other's
// zone shifts its wall-clock date across the offset, so a task scheduled "today"
// would be dropped in a western timezone. sameDay must compare each side's
// intended Y/M/D without conversion.
func TestFilterForDay_TimezoneSkew(t *testing.T) {
	// Task date as qi stores it: UTC midnight on 2026-06-11.
	sched := time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC)
	tasks := []domain.Task{{Text: "scheduled today", Scheduled: &sched}}

	// Target day in a western zone (UTC-7), same calendar date.
	west := time.FixedZone("UTC-7", -7*3600)
	day := time.Date(2026, 6, 11, 9, 0, 0, 0, west)

	got := FilterForDay(tasks, day)
	if len(got) != 1 {
		t.Fatalf("expected the UTC-midnight task to match the local day across the offset, got %d", len(got))
	}

	// And it must NOT match the neighbouring day.
	dayPrev := time.Date(2026, 6, 10, 9, 0, 0, 0, west)
	if n := len(FilterForDay(tasks, dayPrev)); n != 0 {
		t.Fatalf("expected no match on 2026-06-10, got %d", n)
	}
}

func TestFilterDueToday(t *testing.T) {
	now := time.Date(2026, 6, 11, 9, 30, 0, 0, time.Local)
	day := func(y int, m time.Month, d int) *time.Time {
		t := time.Date(y, m, d, 0, 0, 0, 0, time.Local)
		return &t
	}
	today := func() *time.Time { return day(2026, 6, 11) }
	yesterday := func() *time.Time { return day(2026, 6, 10) }
	tomorrow := func() *time.Time { return day(2026, 6, 12) }

	tasks := []domain.Task{
		{Text: "due today", Due: today()},
		{Text: "scheduled today", Scheduled: today()},
		{Text: "both today", Due: today(), Scheduled: today()},
		{Text: "due yesterday", Due: yesterday()},
		{Text: "due tomorrow", Due: tomorrow()},
		{Text: "scheduled tomorrow", Scheduled: tomorrow()},
		{Text: "completed due today", Due: today(), Completed: true},
		{Text: "no dates"},
	}

	got := FilterDueToday(tasks, now)

	want := []string{"due today", "scheduled today", "both today"}
	if len(got) != len(want) {
		t.Fatalf("got %d tasks (%v), want %d (%v)", len(got), texts(got), len(want), want)
	}
	for i, w := range want {
		if got[i].Text != w {
			t.Errorf("got[%d].Text = %q, want %q (order must be preserved)", i, got[i].Text, w)
		}
	}
}

func TestFilterDueToday_Empty(t *testing.T) {
	now := time.Date(2026, 6, 11, 9, 30, 0, 0, time.Local)
	if got := FilterDueToday(nil, now); len(got) != 0 {
		t.Errorf("FilterDueToday(nil) = %v, want empty", got)
	}
}

func texts(tasks []domain.Task) []string {
	out := make([]string, len(tasks))
	for i, t := range tasks {
		out[i] = t.Text
	}
	return out
}
