// Package skills hosts deterministic, composed workflows that qid exposes
// as SourceSkill tools. Each skill chains existing services and reads from
// the vault — never calls an LLM, never writes silently. Skills are
// read-only by default; mutating skills must declare Mutating: true so the
// policy layer can gate them.
package skills

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"qi/internal/domain"
	"qi/internal/service"
	"qi/internal/tools"
)

// DailyReviewToolName is the registered name for the daily-review skill.
const DailyReviewToolName = "skill.daily-review"

const recentCapturesLimit = 5

// dailyReviewOutput is the JSON shape returned by the skill.
type dailyReviewOutput struct {
	Date             string         `json:"date"`
	Events           []dailyEvent   `json:"events"`
	OpenTasks        []dailyTask    `json:"open_tasks"`
	RecentCaptures   []dailyCapture `json:"recent_captures"`
	CalendarWarnings []string       `json:"calendar_warnings,omitempty"`
}

type dailyEvent struct {
	Title   string `json:"title"`
	Start   string `json:"start"`
	End     string `json:"end,omitempty"`
	Project string `json:"project,omitempty"`
}

type dailyTask struct {
	Text    string `json:"text"`
	Project string `json:"project,omitempty"`
	Due     string `json:"due,omitempty"`
}

type dailyCapture struct {
	Path    string `json:"path"`
	Preview string `json:"preview,omitempty"`
}

// RegisterDailyReview binds skill.daily-review against the supplied
// services. inboxDir is scanned for recent captures.
func RegisterDailyReview(r *tools.Registry, tasks service.TaskService, agenda service.AgendaService, inboxDir string) error {
	tool := tools.Tool{
		Name:        DailyReviewToolName,
		Version:     "1",
		Mutating:    false,
		Description: "Summarize today's agenda, open tasks, and recent inbox captures.",
		Schema:      json.RawMessage(`{"type":"object","additionalProperties":false}`),
	}
	handler := func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		now := time.Now()
		out := dailyReviewOutput{
			Date:           now.Format("2006-01-02"),
			Events:         []dailyEvent{},
			OpenTasks:      []dailyTask{},
			RecentCaptures: []dailyCapture{},
		}

		events, warnings, eventsErr := agenda.Today()
		for _, e := range events {
			out.Events = append(out.Events, formatEvent(e))
		}
		for _, w := range warnings {
			out.CalendarWarnings = append(out.CalendarWarnings, w.Error())
		}

		openTasks, tasksErr := tasks.ListOpenTasks()
		for _, t := range openTasks {
			out.OpenTasks = append(out.OpenTasks, formatTask(t))
		}

		captures, capturesErr := recentCaptures(inboxDir, recentCapturesLimit)
		out.RecentCaptures = captures

		if eventsErr != nil && tasksErr != nil && capturesErr != nil {
			return nil, fmt.Errorf("daily-review: all sources failed (agenda=%v, tasks=%v, captures=%v)", eventsErr, tasksErr, capturesErr)
		}
		return json.Marshal(out)
	}
	return r.RegisterSkill(tool, handler)
}

func formatEvent(e domain.Event) dailyEvent {
	out := dailyEvent{
		Title:   e.Title,
		Start:   e.Start.Format("15:04"),
		Project: e.Project,
	}
	if !e.End.IsZero() && !e.End.Equal(e.Start) {
		out.End = e.End.Format("15:04")
	}
	return out
}

func formatTask(t domain.Task) dailyTask {
	out := dailyTask{
		Text:    t.Text,
		Project: t.Project,
	}
	if t.Due != nil {
		out.Due = t.Due.Format("2006-01-02")
	}
	return out
}

func recentCaptures(inboxDir string, limit int) ([]dailyCapture, error) {
	out := []dailyCapture{}
	if inboxDir == "" {
		return out, nil
	}
	entries, err := os.ReadDir(inboxDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return out, fmt.Errorf("read inbox: %w", err)
	}

	type captureMeta struct {
		path    string
		modTime time.Time
	}
	captures := make([]captureMeta, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		captures = append(captures, captureMeta{
			path:    filepath.Join(inboxDir, e.Name()),
			modTime: info.ModTime(),
		})
	}
	sort.Slice(captures, func(i, j int) bool {
		return captures[i].modTime.After(captures[j].modTime)
	})
	if len(captures) > limit {
		captures = captures[:limit]
	}
	for _, c := range captures {
		out = append(out, dailyCapture{
			Path:    c.path,
			Preview: firstBodyLine(c.path),
		})
	}
	return out, nil
}

// firstBodyLine returns the first non-empty content line of an inbox
// capture, skipping the leading timestamp header. Best-effort; errors
// produce an empty preview.
func firstBodyLine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	sawHeader := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !sawHeader {
			// Skip the "2026-05-18 17:16:02" header line WriteCapture emits.
			sawHeader = true
			continue
		}
		if line == "" {
			continue
		}
		return line
	}
	return ""
}
