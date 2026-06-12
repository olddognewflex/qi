package service

import (
	"time"

	"qi/internal/domain"
)

// FilterDueToday returns the open tasks whose Due OR Scheduled date falls on the
// same calendar day as now (compared in now's location). Completed tasks are
// skipped defensively. Input order is preserved, and a task that is both due and
// scheduled today is returned once.
func FilterDueToday(tasks []domain.Task, now time.Time) []domain.Task {
	return FilterForDay(tasks, now)
}

// FilterForDay returns the open tasks whose Due OR Scheduled date falls on the
// same calendar day as day (compared in day's location). Completed tasks are
// skipped defensively. Input order is preserved, and a task that is both due and
// scheduled on day is returned once.
func FilterForDay(tasks []domain.Task, day time.Time) []domain.Task {
	out := make([]domain.Task, 0)
	for _, t := range tasks {
		if t.Completed {
			continue
		}
		if (t.Due != nil && sameDay(*t.Due, day)) ||
			(t.Scheduled != nil && sameDay(*t.Scheduled, day)) {
			out = append(out, t)
		}
	}
	return out
}

// sameDay reports whether a and b denote the same calendar day, each read in its
// OWN location. Task dates are parsed by time.Parse("2006-01-02") at UTC midnight
// (their Y/M/D is the date the user typed), while a target day from time.Now() is
// in Local. Converting one into the other's zone would shift its wall-clock date
// across the UTC offset (e.g. 2026-06-11 00:00 UTC reads as 2026-06-10 in a
// western zone), so compare the intended Y/M/D directly without conversion.
func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
