package vault

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// recurrenceRuleRe matches the supported recurrence grammar:
//
//	every (N )?(day|week|month|year)s?( when done)?
//
// Case-insensitive, tolerant of extra internal whitespace. Rules outside this
// subset (e.g. "every weekday", "every week on monday") do not match and cause
// NextRecurrence to return ok=false.
var recurrenceRuleRe = regexp.MustCompile(`^every\s+(?:(\d+)\s+)?(day|week|month|year)s?(\s+when\s+done)?$`)

// dateOnly truncates t to local midnight in its own location.
func dateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// NextRecurrence computes the next occurrence of a recurring task per the
// Obsidian Tasks model. rule is the raw rule text (without 🔁). due/scheduled are
// the current dates (either may be nil); completedOn is when the task was marked
// done (used only for "when done" rules).
//
// Returns the shifted due/scheduled pointers (only for dates that were present)
// and ok=true when the rule is supported and at least one date exists. Returns
// ok=false (and nil pointers) when the rule is outside the supported subset or
// both dates are nil (Obsidian only recurs dated tasks).
func NextRecurrence(rule string, due, scheduled *time.Time, completedOn time.Time) (nextDue, nextScheduled *time.Time, ok bool) {
	m := recurrenceRuleRe.FindStringSubmatch(strings.TrimSpace(strings.ToLower(rule)))
	if m == nil {
		return nil, nil, false
	}

	n := 1
	if m[1] != "" {
		parsed, err := strconv.Atoi(m[1])
		if err != nil || parsed < 1 {
			return nil, nil, false
		}
		n = parsed
	}
	unit := m[2]
	whenDone := m[3] != ""

	// Reference date: due if present, else scheduled. Both nil → no recurrence.
	var ref time.Time
	switch {
	case due != nil:
		ref = *due
	case scheduled != nil:
		ref = *scheduled
	default:
		return nil, nil, false
	}

	advance := func(t time.Time) time.Time {
		switch unit {
		case "day":
			return t.AddDate(0, 0, n)
		case "week":
			return t.AddDate(0, 0, 7*n)
		case "month":
			return t.AddDate(0, n, 0)
		case "year":
			return t.AddDate(n, 0, 0)
		}
		return t
	}

	// "when done" bases the next occurrence off the completion date; otherwise off
	// the reference date. The delta is then applied uniformly to every present
	// date, preserving the spacing between scheduled and due.
	var newRef time.Time
	if whenDone {
		newRef = advance(dateOnly(completedOn))
	} else {
		newRef = advance(ref)
	}
	delta := newRef.Sub(ref)

	if due != nil {
		shifted := due.Add(delta)
		nextDue = &shifted
	}
	if scheduled != nil {
		shifted := scheduled.Add(delta)
		nextScheduled = &shifted
	}
	return nextDue, nextScheduled, true
}
