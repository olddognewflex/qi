package calendar

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"qi/internal/domain"
)

var (
	eventRangeRe = regexp.MustCompile(`^-\s+(\d{2}:\d{2})-(\d{2}:\d{2})\s+(.+)$`)
	eventTimeRe  = regexp.MustCompile(`^-\s+(\d{2}:\d{2})\s+(.+)$`)
)

type LocalProvider struct {
	// PathFor resolves the daily-note path for a given day (typically
	// config.Config.DailyNotePath). Supports nested, date-formatted layouts.
	PathFor func(time.Time) string
}

func (p LocalProvider) Name() string { return domain.EventSourceLocal }

func (p LocalProvider) Events(from, to time.Time) ([]domain.Event, error) {
	var events []domain.Event

	for d := truncateToDay(from); !d.After(truncateToDay(to)); d = d.AddDate(0, 0, 1) {
		dayEvents, err := p.eventsForDay(d)
		if err != nil {
			continue
		}
		events = append(events, dayEvents...)
	}

	return filterByRange(events, from, to), nil
}

func (p LocalProvider) eventsForDay(day time.Time) ([]domain.Event, error) {
	path := p.PathFor(day)
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	return parseEventsFromNote(f, day)
}

func parseEventsFromNote(r io.Reader, day time.Time) ([]domain.Event, error) {
	scanner := bufio.NewScanner(r)

	inSchedule := false
	var events []domain.Event

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "## ") {
			inSchedule = strings.TrimSpace(strings.TrimPrefix(line, "##")) == "Schedule"
			continue
		}
		if !inSchedule {
			continue
		}
		if strings.HasPrefix(line, "#") {
			inSchedule = false
			continue
		}

		ev, ok := parseLine(line, day)
		if ok {
			events = append(events, ev)
		}
	}

	return events, scanner.Err()
}

func parseLine(line string, day time.Time) (domain.Event, bool) {
	if m := eventRangeRe.FindStringSubmatch(line); m != nil {
		title, project := extractProject(strings.TrimSpace(m[3]))
		start, err1 := parseTimeOnDay(m[1], day)
		end, err2 := parseTimeOnDay(m[2], day)
		if err1 != nil || err2 != nil {
			return domain.Event{}, false
		}
		return domain.Event{
			ID:      fmt.Sprintf("local-%s-%s", day.Format("2006-01-02"), m[1]),
			Source:  domain.EventSourceLocal,
			Title:   title,
			Start:   start,
			End:     end,
			Project: project,
		}, true
	}

	if m := eventTimeRe.FindStringSubmatch(line); m != nil {
		title, project := extractProject(strings.TrimSpace(m[2]))
		start, err := parseTimeOnDay(m[1], day)
		if err != nil {
			return domain.Event{}, false
		}
		return domain.Event{
			ID:      fmt.Sprintf("local-%s-%s", day.Format("2006-01-02"), m[1]),
			Source:  domain.EventSourceLocal,
			Title:   title,
			Start:   start,
			End:     start.Add(time.Hour),
			Project: project,
		}, true
	}

	return domain.Event{}, false
}

func extractProject(s string) (title, project string) {
	parts := strings.Fields(s)
	for i := len(parts) - 1; i >= 0; i-- {
		if strings.HasPrefix(parts[i], "#") {
			project = strings.TrimPrefix(parts[i], "#")
			title = strings.Join(parts[:i], " ")
			if title == "" {
				title = s
				project = ""
			}
			return
		}
	}
	return s, ""
}

func parseTimeOnDay(hhmm string, day time.Time) (time.Time, error) {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return time.Time{}, err
	}
	return time.Date(day.Year(), day.Month(), day.Day(), t.Hour(), t.Minute(), 0, 0, day.Location()), nil
}

func truncateToDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func filterByRange(events []domain.Event, from, to time.Time) []domain.Event {
	var out []domain.Event
	for _, e := range events {
		if !e.Start.Before(from) && !e.Start.After(to) {
			out = append(out, e)
		}
	}
	return out
}
