package service

import (
	"sort"
	"strings"

	"qi/internal/calendar"
	"qi/internal/domain"
)

// PlanInput drives PlanBlocks. Tasks are the candidates to time-block; Existing
// is the current ## Schedule (parsed) used both to avoid double-booking slots
// and to skip tasks already on the schedule. StartMin is the earliest minute of
// the day to place into, BlockMin the length of each block, Limit the maximum
// number of tasks to place.
type PlanInput struct {
	Tasks    []domain.Task
	Existing []calendar.ScheduleEntry
	StartMin int
	BlockMin int
	Limit    int
}

// interval is a half-open [start, end) span of occupied minutes.
type interval struct {
	start, end int
}

// PlanBlocks assigns each candidate task (up to Limit) a free time block,
// returning only the NEW entries in placement order. It is pure: it never reads
// or writes the vault.
//
// Idempotency: a task whose title already appears among Existing entries
// (case-insensitive, trimmed) is skipped, so re-running plan does not duplicate
// blocks. Placement walks a cursor forward from StartMin, never backwards,
// finding the first BlockMin-long slot that overlaps no occupied interval;
// placed slots join the occupied set so later tasks pack in after them.
func PlanBlocks(in PlanInput) []calendar.ScheduleEntry {
	occupied := make([]interval, 0, len(in.Existing))
	scheduled := make(map[string]bool, len(in.Existing))
	for _, e := range in.Existing {
		occupied = append(occupied, interval{e.StartMin, e.EndMin})
		scheduled[normalizeTitle(e.Title)] = true
	}

	var planned []calendar.ScheduleEntry
	cursor := in.StartMin
	for _, task := range in.Tasks {
		if in.Limit > 0 && len(planned) >= in.Limit {
			break
		}
		title, project := splitTaskTitle(task)
		if scheduled[normalizeTitle(title)] {
			continue
		}

		start := nextFreeSlot(cursor, in.BlockMin, occupied)
		end := start + in.BlockMin
		entry := calendar.ScheduleEntry{StartMin: start, EndMin: end, Title: title, Project: project}
		planned = append(planned, entry)
		occupied = append(occupied, interval{start, end})
		scheduled[normalizeTitle(title)] = true
		cursor = end
	}
	return planned
}

// nextFreeSlot returns the start minute of the first [c, c+block) span at or
// after cursor that overlaps no occupied interval. On overlap the cursor jumps
// to the end of the blocking interval and retries.
func nextFreeSlot(cursor, block int, occupied []interval) int {
	c := cursor
	for {
		end := c + block
		moved := false
		for _, iv := range occupied {
			if c < iv.end && iv.start < end {
				c = iv.end
				moved = true
				break
			}
		}
		if !moved {
			return c
		}
	}
}

// MergeAndRender concatenates existing and planned entries, sorts them by start
// time (stable), and joins their rendered lines with newlines. The result is the
// new ## Schedule body.
func MergeAndRender(existing, planned []calendar.ScheduleEntry) string {
	all := make([]calendar.ScheduleEntry, 0, len(existing)+len(planned))
	all = append(all, existing...)
	all = append(all, planned...)
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].StartMin < all[j].StartMin
	})
	lines := make([]string, 0, len(all))
	for _, e := range all {
		lines = append(lines, e.Render())
	}
	return strings.Join(lines, "\n")
}

// splitTaskTitle derives a task's display title and project. ParseTaskLine
// leaves the project tag inline in Text (e.g. "Buy milk #home"); strip a
// trailing " #<Project>" so ScheduleEntry.Render re-adds it cleanly via Project.
func splitTaskTitle(t domain.Task) (title, project string) {
	title = strings.TrimSpace(t.Text)
	if t.Project != "" {
		title = strings.TrimSpace(strings.TrimSuffix(title, "#"+t.Project))
	}
	return title, t.Project
}

// normalizeTitle lowercases and trims a title for dedup comparison.
func normalizeTitle(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
