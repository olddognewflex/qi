package service

import (
	"testing"
	"time"

	"qi/internal/domain"
)

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
