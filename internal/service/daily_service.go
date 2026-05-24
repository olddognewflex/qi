package service

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"qi/internal/domain"
)

// DailyService owns the daily-note workflow business logic: ensuring the note
// exists (triggering Obsidian and polling for it), rendering the agenda, and
// formatting checkpoints. Commands stay thin and delegate here.
type DailyService struct {
	PathFor    func(time.Time) string // resolves the daily-note path (config.DailyNotePath)
	Agenda     AgendaService
	VaultName  string             // vault name for the obsidian:// URI
	OpenFunc   func(string) error // opens a URI; nil-safe (no-op) for tests
	CreateWait time.Duration      // max time to poll for note creation (default 3s)
}

const defaultCreateWait = 3 * time.Second

// EnsureNote returns the path to the daily note for day. If the note does not
// exist, it fires the Advanced URI daily-creation trigger and polls for the
// file to appear, up to CreateWait. Returns an error if it never appears.
func (s DailyService) EnsureNote(day time.Time) (string, error) {
	path := s.PathFor(day)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}

	uri := "obsidian://adv-uri?vault=" + url.QueryEscape(s.VaultName) + "&daily=true"
	if s.OpenFunc != nil {
		if err := s.OpenFunc(uri); err != nil {
			return "", fmt.Errorf("trigger daily note creation: %w", err)
		}
	}

	wait := s.CreateWait
	if wait <= 0 {
		wait = defaultCreateWait
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	return "", fmt.Errorf(
		"daily note not created at %s — install the Advanced URI Obsidian plugin or open the note manually",
		path,
	)
}

// RenderAgenda formats events into markdown agenda lines, e.g.
// "- 09:00–10:00 Title #project" or "- 14:00 Title" (en-dash between times;
// end omitted when zero or equal to start; project prefixed as #project).
func (s DailyService) RenderAgenda(events []domain.Event) string {
	if len(events) == 0 {
		return "No events."
	}
	var b strings.Builder
	for i, e := range events {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("- ")
		b.WriteString(e.Start.Format("15:04"))
		if !e.End.IsZero() && e.End != e.Start {
			b.WriteString("–")
			b.WriteString(e.End.Format("15:04"))
		}
		b.WriteString(" ")
		b.WriteString(e.Title)
		if e.Project != "" {
			b.WriteString(" #")
			b.WriteString(e.Project)
		}
	}
	return b.String()
}
