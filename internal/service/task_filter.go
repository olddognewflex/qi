package service

import (
	"cmp"
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"strings"
	"time"

	"qi/internal/domain"
)

var dataviewNumberRe = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)

const maxNumericPriorityLength = 128

type priorityValueKind uint8

const (
	priorityEmpty priorityValueKind = iota
	priorityNumber
	priorityText
)

type prioritySortKey struct {
	kind   priorityValueKind
	number *big.Rat
	text   string
}

// TaskFilter narrows the tasks returned by ListTasks. The zero value lists open
// tasks with no project or date constraint (the historic ListOpenTasks
// behavior). Filters AND-narrow: a task must satisfy every set field.
type TaskFilter struct {
	// Project keeps only tasks whose project tag matches, case-insensitive.
	// Empty matches any project.
	Project string
	// Status selects by completion state: "open" (default when empty), "done",
	// or "all". Any other value is an error.
	Status string
	// Date keeps tasks whose Scheduled OR Due date matches: "today", "overdue"
	// (scheduled/due strictly before Now), or an absolute YYYY-MM-DD. Empty
	// applies no date filter.
	Date string
	// Before keeps tasks whose Scheduled OR Due date falls strictly before this
	// day. Nil applies no bound.
	Before *time.Time
	// After keeps tasks whose Scheduled OR Due date falls strictly after this
	// day. Nil applies no bound.
	After *time.Time
	// ActionableOn keeps open tasks whose Due and Scheduled dates are each
	// either absent or no later than this day. Nil applies no actionable filter.
	ActionableOn *time.Time
	// Now is the reference day for "today"/"overdue". Zero means time.Now().
	Now time.Time
}

// ListTasks aggregates every task and returns those matching f. Filters compose
// with AND semantics; the result preserves aggregation order. A nil/empty filter
// is equivalent to ListOpenTasks.
func (s TaskService) ListTasks(f TaskFilter) ([]domain.Task, error) {
	all, err := s.ListAllTasks()
	if err != nil {
		return nil, err
	}

	now := f.Now
	if now.IsZero() {
		now = time.Now()
	}

	status := strings.ToLower(strings.TrimSpace(f.Status))
	if status == "" {
		status = "open"
	}
	switch status {
	case "open", "done", "all":
	default:
		return nil, fmt.Errorf("invalid status %q: use open, done, or all", f.Status)
	}

	dateMatch, err := f.dateMatcher(now)
	if err != nil {
		return nil, err
	}

	project := strings.ToLower(strings.TrimSpace(f.Project))

	out := make([]domain.Task, 0, len(all))
	for _, t := range all {
		switch status {
		case "open":
			if t.Completed {
				continue
			}
		case "done":
			if !t.Completed {
				continue
			}
		}
		if project != "" && strings.ToLower(t.Project) != project {
			continue
		}
		if dateMatch != nil && !dateMatch(t) {
			continue
		}
		if f.Before != nil && !anyDate(t, func(d time.Time) bool { return dayBefore(d, *f.Before) }) {
			continue
		}
		if f.After != nil && !anyDate(t, func(d time.Time) bool { return dayBefore(*f.After, d) }) {
			continue
		}
		if f.ActionableOn != nil && !actionableOn(t, *f.ActionableOn) {
			continue
		}
		out = append(out, t)
	}
	if f.ActionableOn != nil {
		priorityKeys := make(map[string]prioritySortKey)
		for _, task := range out {
			if _, ok := priorityKeys[task.Priority]; !ok {
				priorityKeys[task.Priority] = newPrioritySortKey(task.Priority)
			}
		}
		slices.SortStableFunc(out, func(a, b domain.Task) int {
			return comparePrioritySortKeys(priorityKeys[a.Priority], priorityKeys[b.Priority])
		})
	}
	return out, nil
}

// dateMatcher compiles the Date field into a predicate over a task's
// Scheduled/Due dates, or nil when no date filter is set.
func (f TaskFilter) dateMatcher(now time.Time) (func(domain.Task) bool, error) {
	switch strings.ToLower(strings.TrimSpace(f.Date)) {
	case "":
		return nil, nil
	case "today":
		return func(t domain.Task) bool {
			return anyDate(t, func(d time.Time) bool { return sameDay(d, now) })
		}, nil
	case "overdue":
		return func(t domain.Task) bool {
			return anyDate(t, func(d time.Time) bool { return dayBefore(d, now) })
		}, nil
	default:
		parsed, err := time.Parse("2006-01-02", strings.TrimSpace(f.Date))
		if err != nil {
			return nil, fmt.Errorf("invalid date %q: use today, overdue, or YYYY-MM-DD", f.Date)
		}
		return func(t domain.Task) bool {
			return anyDate(t, func(d time.Time) bool { return sameDay(d, parsed) })
		}, nil
	}
}

func comparePrioritySortKeys(aKey, bKey prioritySortKey) int {
	if aKey.kind != bKey.kind {
		return cmp.Compare(bKey.kind, aKey.kind)
	}
	switch aKey.kind {
	case priorityNumber:
		return bKey.number.Cmp(aKey.number)
	case priorityText:
		return strings.Compare(bKey.text, aKey.text)
	case priorityEmpty:
		return 0
	default:
		return 0
	}
}

func newPrioritySortKey(raw string) prioritySortKey {
	if raw == "" {
		return prioritySortKey{kind: priorityEmpty}
	}
	if len(raw) <= maxNumericPriorityLength && dataviewNumberRe.MatchString(raw) {
		if number, ok := new(big.Rat).SetString(raw); ok {
			return prioritySortKey{kind: priorityNumber, number: number}
		}
	}
	return prioritySortKey{kind: priorityText, text: raw}
}

func actionableOn(t domain.Task, day time.Time) bool {
	return !t.Completed &&
		(t.Due == nil || !dayBefore(day, *t.Due)) &&
		(t.Scheduled == nil || !dayBefore(day, *t.Scheduled))
}

// anyDate reports whether the task's Scheduled or Due date satisfies pred. A nil
// date is skipped; a task with neither date never matches.
func anyDate(t domain.Task, pred func(time.Time) bool) bool {
	if t.Scheduled != nil && pred(*t.Scheduled) {
		return true
	}
	if t.Due != nil && pred(*t.Due) {
		return true
	}
	return false
}

// dayBefore reports whether a's calendar day precedes b's, each read in its own
// location (see sameDay for why no zone conversion happens).
func dayBefore(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	if ay != by {
		return ay < by
	}
	if am != bm {
		return am < bm
	}
	return ad < bd
}
