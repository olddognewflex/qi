package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"qi/internal/service"
	"qi/internal/tools"
	"qi/internal/vault"
)

// WeeklyReviewToolName is the registered name for the read-only weekly-review
// skill. It aggregates the week's completed tasks (by their ✅ done-date),
// capture volume (inbox + archive, by filename date), and daily ## Logs
// highlights, and returns a proposed review note (title + markdown body). It is
// purely read-only — it never writes to the vault. Writing the proposed note
// goes through the separate, mutating WeeklyReviewApply skill so the policy
// layer can gate it.
const WeeklyReviewToolName = "skill.weekly-review"

// WeeklyReviewApplyToolName is the registered name for the mutating companion
// skill that writes a title+body as a note. It is declared Mutating so non-cli
// callers (ai-planner, mcp) route through the approval queue.
const WeeklyReviewApplyToolName = "skill.weekly-review-apply"

// defaultWeeklyReviewDays is the window size used when params omit "days".
const defaultWeeklyReviewDays = 7

// captureDateRE matches the leading YYYY-MM-DD of a capture filename.
var captureDateRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}`)

// weeklyReviewOutput is the JSON shape returned by skill.weekly-review.
type weeklyReviewOutput struct {
	WeekStart     string            `json:"week_start"` // YYYY-MM-DD
	WeekEnd       string            `json:"week_end"`   // YYYY-MM-DD
	Completed     []weeklyTask      `json:"completed"`  // tasks done in the window
	CompletedN    int               `json:"completed_count"`
	CaptureTotal  int               `json:"capture_total"`
	CapturesByDay []captureDayCount `json:"captures_by_day"`
	Highlights    []dailyHighlight  `json:"highlights"` // days with non-empty ## Logs
	ProposedTitle string            `json:"proposed_title"`
	ProposedBody  string            `json:"proposed_body"` // rendered markdown review note
}

type weeklyTask struct {
	Text    string `json:"text"`
	Project string `json:"project,omitempty"`
	Done    string `json:"done"` // YYYY-MM-DD
}

type captureDayCount struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

type dailyHighlight struct {
	Date string `json:"date"`
	Logs string `json:"logs"`
}

// weeklyReviewInput is the JSON shape skill.weekly-review accepts. Both fields
// are optional; an empty body uses the defaults (end = today, days = 7).
type weeklyReviewInput struct {
	End  string `json:"end,omitempty"`  // YYYY-MM-DD, default today
	Days int    `json:"days,omitempty"` // default 7
}

// RegisterWeeklyReview binds the read-only skill.weekly-review tool. inboxDir
// and archiveDir are scanned (non-recursively) for *.md captures; dailyPathFor
// resolves the daily-note path for a given day's ## Logs highlights.
func RegisterWeeklyReview(r *tools.Registry, tasks service.TaskService, inboxDir, archiveDir string, dailyPathFor func(time.Time) string) error {
	tool := tools.Tool{
		Name:        WeeklyReviewToolName,
		Version:     "1",
		Mutating:    false,
		Description: "Aggregate the week's completed tasks, capture volume, and daily-log highlights into a proposed review note.",
		Schema: json.RawMessage(`{"type":"object","additionalProperties":false,` +
			`"properties":{` +
			`"end":{"type":"string","description":"window end date YYYY-MM-DD (default today)"},` +
			`"days":{"type":"integer","description":"window length in days (default 7)"}}}`),
	}
	handler := func(_ context.Context, params json.RawMessage) (json.RawMessage, error) {
		var in weeklyReviewInput
		if len(params) > 0 {
			if err := json.Unmarshal(params, &in); err != nil {
				return nil, fmt.Errorf("weekly-review: invalid params: %w", err)
			}
		}

		end := time.Now()
		if strings.TrimSpace(in.End) != "" {
			parsed, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(in.End), time.Local)
			if err != nil {
				return nil, fmt.Errorf("weekly-review: invalid end date %q: %w", in.End, err)
			}
			end = parsed
		}
		days := in.Days
		if days <= 0 {
			days = defaultWeeklyReviewDays
		}

		out, err := proposeWeeklyReview(tasks, inboxDir, archiveDir, dailyPathFor, end, days)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	}
	return r.RegisterSkill(tool, handler)
}

// proposeWeeklyReview computes the weekly review over the inclusive calendar-day
// window ending at end and spanning days. A failure reading tasks is fatal;
// missing inbox/archive/daily files are not.
func proposeWeeklyReview(tasks service.TaskService, inboxDir, archiveDir string, dailyPathFor func(time.Time) string, end time.Time, days int) (weeklyReviewOutput, error) {
	if days <= 0 {
		days = defaultWeeklyReviewDays
	}
	// Truncate to calendar dates; window is the inclusive range [start, end].
	end = truncDate(end)
	start := end.AddDate(0, 0, -(days - 1))

	out := weeklyReviewOutput{
		WeekStart:     start.Format("2006-01-02"),
		WeekEnd:       end.Format("2006-01-02"),
		Completed:     []weeklyTask{},
		CapturesByDay: []captureDayCount{},
		Highlights:    []dailyHighlight{},
	}

	// Completed tasks done within the window.
	all, err := tasks.ListAllTasks()
	if err != nil {
		return weeklyReviewOutput{}, fmt.Errorf("weekly-review: list tasks: %w", err)
	}
	for _, t := range all {
		if !t.Completed || t.CompletedAt == nil {
			continue
		}
		if !dayInRange(*t.CompletedAt, start, end) {
			continue
		}
		out.Completed = append(out.Completed, weeklyTask{
			Text:    t.Text,
			Project: t.Project,
			Done:    t.CompletedAt.Format("2006-01-02"),
		})
	}
	sort.Slice(out.Completed, func(i, j int) bool {
		if out.Completed[i].Done != out.Completed[j].Done {
			return out.Completed[i].Done < out.Completed[j].Done
		}
		return out.Completed[i].Text < out.Completed[j].Text
	})
	out.CompletedN = len(out.Completed)

	// Capture volume across the inbox and its archive.
	byDay := map[string]int{}
	countCaptures(inboxDir, start, end, byDay)
	countCaptures(archiveDir, start, end, byDay)
	dates := make([]string, 0, len(byDay))
	for d := range byDay {
		dates = append(dates, d)
	}
	sort.Strings(dates)
	for _, d := range dates {
		out.CapturesByDay = append(out.CapturesByDay, captureDayCount{Date: d, Count: byDay[d]})
		out.CaptureTotal += byDay[d]
	}

	// Daily highlights: any in-window day with a non-empty ## Logs section.
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		if dailyPathFor == nil {
			break
		}
		body, found, readErr := vault.ReadSection(dailyPathFor(d), "Logs")
		if readErr != nil || !found {
			continue
		}
		if strings.TrimSpace(body) == "" {
			continue
		}
		out.Highlights = append(out.Highlights, dailyHighlight{
			Date: d.Format("2006-01-02"),
			Logs: body,
		})
	}

	out.ProposedTitle = fmt.Sprintf("Weekly Review %s — %s", out.WeekStart, out.WeekEnd)
	out.ProposedBody = renderWeeklyReview(out)
	return out, nil
}

// countCaptures buckets *.md captures in dir (non-recursively) by their
// filename date when it parses and falls within [start, end]. A missing dir is
// not an error.
func countCaptures(dir string, start, end time.Time, byDay map[string]int) {
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // missing/unreadable dir: skip silently, per the read-only contract
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		name := e.Name()
		if len(name) < 10 {
			continue
		}
		datePart := name[:10]
		if !captureDateRE.MatchString(datePart) {
			continue
		}
		d, err := time.ParseInLocation("2006-01-02", datePart, time.Local)
		if err != nil {
			continue
		}
		if !dayInRange(d, start, end) {
			continue
		}
		byDay[datePart]++
	}
}

// renderWeeklyReview produces the deterministic markdown review note body that
// the apply step writes.
func renderWeeklyReview(out weeklyReviewOutput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Weekly Review %s — %s\n\n", out.WeekStart, out.WeekEnd)

	fmt.Fprintf(&b, "## Completed (%d)\n", out.CompletedN)
	if len(out.Completed) == 0 {
		b.WriteString("_None._\n")
	} else {
		for _, t := range out.Completed {
			// ParseTaskLine leaves the project tag inline in Text
			// (e.g. "Ship it #qi"), so only append "#<project>" when the
			// text does not already carry it — avoids a duplicate tag.
			if t.Project != "" && !strings.Contains(t.Text, "#"+t.Project) {
				fmt.Fprintf(&b, "- %s #%s — %s\n", t.Text, t.Project, t.Done)
			} else {
				fmt.Fprintf(&b, "- %s — %s\n", t.Text, t.Done)
			}
		}
	}
	b.WriteString("\n")

	fmt.Fprintf(&b, "## Captures (%d)\n", out.CaptureTotal)
	if len(out.CapturesByDay) == 0 {
		b.WriteString("_None._\n")
	} else {
		for _, c := range out.CapturesByDay {
			fmt.Fprintf(&b, "- %s: %d\n", c.Date, c.Count)
		}
	}
	b.WriteString("\n")

	b.WriteString("## Daily highlights\n")
	if len(out.Highlights) == 0 {
		b.WriteString("_No log entries._\n")
	} else {
		for _, h := range out.Highlights {
			fmt.Fprintf(&b, "### %s\n%s\n", h.Date, h.Logs)
		}
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

// truncDate returns t with the time-of-day stripped (midnight in t's own zone).
func truncDate(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// dateKey collapses t to an integer YYYYMMDD in t's OWN location, so dates can
// be compared without crossing a zone offset.
func dateKey(t time.Time) int {
	y, m, d := t.Date()
	return y*10000 + int(m)*100 + d
}

// dayInRange reports whether t's calendar day falls within the inclusive range
// [start, end]. Each value's intended Y/M/D is compared in its OWN location:
// CompletedAt is UTC midnight (time.Parse), while the window bounds are Local
// (time.Now), so comparing instants would shift a date across the offset and
// drop boundary-day tasks in non-UTC zones. (Same hazard as service.sameDay.)
func dayInRange(t, start, end time.Time) bool {
	k := dateKey(t)
	return k >= dateKey(start) && k <= dateKey(end)
}

// weeklyReviewApplyInput is the JSON shape skill.weekly-review-apply accepts.
type weeklyReviewApplyInput struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// weeklyReviewApplyOutput reports what the mutation created.
type weeklyReviewApplyOutput struct {
	Created string `json:"created"`
	Title   string `json:"title"`
}

// RegisterWeeklyReviewApply binds the mutating skill.weekly-review-apply tool.
// It writes a given title+body as a note via the NoteService.
func RegisterWeeklyReviewApply(r *tools.Registry, notes service.NoteService) error {
	tool := tools.Tool{
		Name:        WeeklyReviewApplyToolName,
		Version:     "1",
		Mutating:    true,
		Description: "Write a weekly-review proposal (title + markdown body) to the vault as a note.",
		Schema: json.RawMessage(`{"type":"object","additionalProperties":false,` +
			`"required":["title","body"],` +
			`"properties":{` +
			`"title":{"type":"string","description":"note title"},` +
			`"body":{"type":"string","description":"markdown note body"}}}`),
	}
	handler := func(_ context.Context, params json.RawMessage) (json.RawMessage, error) {
		var in weeklyReviewApplyInput
		if len(params) > 0 {
			if err := json.Unmarshal(params, &in); err != nil {
				return nil, fmt.Errorf("weekly-review-apply: invalid params: %w", err)
			}
		}
		out, err := applyWeeklyReview(notes, in)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	}
	return r.RegisterSkill(tool, handler)
}

// applyWeeklyReview writes the supplied title+body as a note and reports where
// it landed. Empty title or body is rejected.
func applyWeeklyReview(notes service.NoteService, in weeklyReviewApplyInput) (weeklyReviewApplyOutput, error) {
	title := strings.TrimSpace(in.Title)
	body := strings.TrimSpace(in.Body)
	if title == "" {
		return weeklyReviewApplyOutput{}, fmt.Errorf("weekly-review-apply: title is required")
	}
	if body == "" {
		return weeklyReviewApplyOutput{}, fmt.Errorf("weekly-review-apply: body is required")
	}
	note, err := notes.AddNote(title, body)
	if err != nil {
		return weeklyReviewApplyOutput{}, fmt.Errorf("weekly-review-apply: write note: %w", err)
	}
	return weeklyReviewApplyOutput{Created: note.Path, Title: title}, nil
}
