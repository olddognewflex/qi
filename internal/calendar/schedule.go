package calendar

import (
	"fmt"
	"strings"
)

// ScheduleEntry is a single line of a daily note's "## Schedule" block, parsed
// into minutes-since-midnight. It is the day-agnostic counterpart to
// domain.Event (which LocalProvider builds by binding these times to a day):
// the same eventRangeRe/eventTimeRe regexes parse both. StartMin/EndMin are
// minutes since midnight (0–1439); EndMin may exceed 1439 for a range that
// crosses midnight, which callers are not expected to produce.
type ScheduleEntry struct {
	StartMin int
	EndMin   int
	Title    string
	Project  string
}

// ParseScheduleEntries parses the raw body of a "## Schedule" section (one entry
// per line) into ScheduleEntry values, in input order. A range line
// ("- HH:MM-HH:MM Title") yields its two times; a single-time line
// ("- HH:MM Title") yields EndMin = StartMin + 60, matching LocalProvider's
// one-hour default. Non-matching lines are skipped.
func ParseScheduleEntries(body string) []ScheduleEntry {
	var out []ScheduleEntry
	for _, line := range strings.Split(body, "\n") {
		if m := eventRangeRe.FindStringSubmatch(line); m != nil {
			start, ok1 := hhmmToMin(m[1])
			end, ok2 := hhmmToMin(m[2])
			if !ok1 || !ok2 {
				continue
			}
			title, project := extractProject(strings.TrimSpace(m[3]))
			out = append(out, ScheduleEntry{StartMin: start, EndMin: end, Title: title, Project: project})
			continue
		}
		if m := eventTimeRe.FindStringSubmatch(line); m != nil {
			start, ok := hhmmToMin(m[1])
			if !ok {
				continue
			}
			title, project := extractProject(strings.TrimSpace(m[2]))
			out = append(out, ScheduleEntry{StartMin: start, EndMin: start + 60, Title: title, Project: project})
		}
	}
	return out
}

// Render renders an entry as an ASCII-hyphen range line ("- HH:MM-HH:MM Title"),
// appending " #project" when Project is set. The ASCII hyphen is load-bearing:
// it is what eventRangeRe matches, so a rendered line round-trips through the
// LocalProvider into qi agenda. (Do not use the en-dash that RenderAgenda uses
// for ## Agenda.)
func (e ScheduleEntry) Render() string {
	line := "- " + minToHHMM(e.StartMin) + "-" + minToHHMM(e.EndMin) + " " + e.Title
	if e.Project != "" {
		line += " #" + e.Project
	}
	return line
}

// hhmmToMin parses "HH:MM" into minutes since midnight.
func hhmmToMin(hhmm string) (int, bool) {
	var h, m int
	if _, err := fmt.Sscanf(hhmm, "%d:%d", &h, &m); err != nil {
		return 0, false
	}
	return h*60 + m, true
}

// minToHHMM formats minutes since midnight as zero-padded "HH:MM".
func minToHHMM(min int) string {
	return fmt.Sprintf("%02d:%02d", min/60, min%60)
}
