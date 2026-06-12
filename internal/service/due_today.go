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
	out := make([]domain.Task, 0)
	for _, t := range tasks {
		if t.Completed {
			continue
		}
		if (t.Due != nil && sameDay(*t.Due, now)) ||
			(t.Scheduled != nil && sameDay(*t.Scheduled, now)) {
			out = append(out, t)
		}
	}
	return out
}

// sameDay reports whether a and b fall on the same calendar day, comparing in
// b's location. Date-granularity task times are parsed at local midnight, so a
// location-normalised Y/M/D comparison is sufficient.
func sameDay(a, b time.Time) bool {
	a = a.In(b.Location())
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
