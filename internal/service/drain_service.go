package service

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// RemoteTask is one task pulled from the cloud queue. It mirrors the queue's
// wire shape but is defined here so the service layer does not depend on the
// remotequeue or config packages — the command layer adapts the concrete client
// to this type. project and client are separate (as they arrive from the
// cloud); the drainer validates and routes them.
type RemoteTask struct {
	ID        string
	Text      string
	Kind      string // "task" (default) | "note" | "capture"; drives drain routing
	Project   string
	Client    string
	Due       string // "YYYY-MM-DD" or ""
	Scheduled string // "YYYY-MM-DD" or ""
}

// RemoteQueue is the transport the drainer needs. The command layer supplies a
// concrete implementation (internal/remotequeue) adapted to RemoteTask. Keeping
// it an interface keeps the service layer free of HTTP and config deps and
// makes the drainer trivially testable with a fake.
type RemoteQueue interface {
	Pull(ctx context.Context, limit int) ([]RemoteTask, error)
	Ack(ctx context.Context, ids []string) error
	Deadletter(ctx context.Context, ids []string, reason string) error
}

// DrainService performs one drain pass: pull remote tasks, validate each, write
// the valid ones idempotently into the vault, ack what was written, and
// deadletter what failed. It never silently drops a task — every pulled row is
// either acked (written) or deadlettered (rejected).
type DrainService struct {
	Tasks    TaskService
	Notes    NoteService    // sink for kind="note" rows
	Captures CaptureService // sink for kind="capture" rows
	Queue    RemoteQueue
	IsClient func(string) bool // closure over Config.ClientByName; may be nil
}

// DrainFailure records one task that could not be written, for deadlettering
// and for the --show-failed summary.
type DrainFailure struct {
	ID     string
	Reason string
}

// DrainSummary reports the outcome of a drain pass.
type DrainSummary struct {
	Drained  int // tasks written (or already present) and acked
	Rejected int // tasks that failed validation and were deadlettered
	Failures []DrainFailure
}

// Drain runs a single pass. Ordering is strict: write → ack (never ack → write)
// so a dropped ack is safe — the next pass re-pulls the same row and the
// idempotency guard in CreateTask skips re-writing, then re-acks. A pull failure
// returns the error with nothing written.
func (s DrainService) Drain(ctx context.Context, limit int) (DrainSummary, error) {
	if limit <= 0 {
		limit = 100
	}

	tasks, err := s.Queue.Pull(ctx, limit)
	if err != nil {
		return DrainSummary{}, fmt.Errorf("drain: pull: %w", err)
	}

	var summary DrainSummary
	var writtenIDs []string

	for _, rt := range tasks {
		// Route by kind BEFORE task-specific validation. project/client/dates only
		// apply to tasks; note/capture rows ignore them entirely. Unlike tasks,
		// note/capture writes are NOT id-idempotent (the vault has no dedup against
		// the queue id), so a rare ack-failure re-drain can duplicate them — that
		// is acceptable: these writes are cheap and reversible, and ack failures
		// are rare. Tasks remain idempotent via their provided id.
		kind := strings.TrimSpace(rt.Kind)
		if kind == "" {
			kind = "task"
		}

		switch kind {
		case "task":
			// project and client arrive separately from the cloud; validate the raw
			// pair (mutual exclusion, charset, client-against-config) before routing.
			input := AddTaskInput{Text: rt.Text}
			if err := ValidateAddInput(input, rt.Project, rt.Client, s.IsClient); err != nil {
				summary.Failures = append(summary.Failures, DrainFailure{ID: rt.ID, Reason: err.Error()})
				continue
			}

			due, err := parseDrainDate("due", rt.Due)
			if err != nil {
				summary.Failures = append(summary.Failures, DrainFailure{ID: rt.ID, Reason: err.Error()})
				continue
			}
			scheduled, err := parseDrainDate("scheduled", rt.Scheduled)
			if err != nil {
				summary.Failures = append(summary.Failures, DrainFailure{ID: rt.ID, Reason: err.Error()})
				continue
			}

			// A configured client name routes to its own tag (mirrors `qi task add
			// --client`). Otherwise the free-form project tag is used.
			tag := strings.TrimSpace(rt.Project)
			if c := strings.TrimSpace(rt.Client); c != "" {
				tag = c
			}

			if _, err := s.Tasks.CreateTask(AddTaskInput{
				Text:      rt.Text,
				Project:   tag,
				Due:       due,
				Scheduled: scheduled,
				ID:        rt.ID,
			}); err != nil {
				// A write failure is not a validation rejection — surface it so the
				// command exits non-zero, and do NOT ack (the row stays pending for
				// the next pass). Re-pull + idempotency keeps that safe.
				return summary, fmt.Errorf("drain: write task %s: %w", rt.ID, err)
			}

		case "note":
			if _, err := s.Notes.AddNote(rt.Text, ""); err != nil {
				// Same fatal handling as the task write path: surface the error and
				// do NOT ack so the row stays pending for the next pass.
				return summary, fmt.Errorf("drain: write note %s: %w", rt.ID, err)
			}

		case "capture":
			if err := s.Captures.Capture(rt.Text); err != nil {
				return summary, fmt.Errorf("drain: write capture %s: %w", rt.ID, err)
			}

		default:
			// An unknown kind is a validation rejection, not a silent drop — it
			// deadletters so it is kept for review.
			summary.Failures = append(summary.Failures, DrainFailure{ID: rt.ID, Reason: fmt.Sprintf("unknown kind %q", kind)})
			continue
		}

		writtenIDs = append(writtenIDs, rt.ID)
	}

	// Write succeeded for these — ack them (delete from the cloud). An ack
	// failure here is reported but not fatal: the rows stay pending and the next
	// pass dedups them. Order matters: ack only after all writes are durable.
	if err := s.Queue.Ack(ctx, writtenIDs); err != nil {
		return summary, fmt.Errorf("drain: ack: %w", err)
	}
	summary.Drained = len(writtenIDs)

	// Deadletter the validation failures so they are kept for review, never
	// silently dropped.
	if len(summary.Failures) > 0 {
		ids := make([]string, 0, len(summary.Failures))
		var reasons []string
		for _, f := range summary.Failures {
			ids = append(ids, f.ID)
			reasons = append(reasons, fmt.Sprintf("%s: %s", f.ID, f.Reason))
		}
		if err := s.Queue.Deadletter(ctx, ids, strings.Join(reasons, "; ")); err != nil {
			return summary, fmt.Errorf("drain: deadletter: %w", err)
		}
		summary.Rejected = len(summary.Failures)
	}

	return summary, nil
}

// parseDrainDate parses an optional "YYYY-MM-DD" string from the cloud. An empty
// string yields nil (no date). A malformed string is a validation-style failure
// surfaced to the caller as a rejection reason.
func parseDrainDate(field, v string) (*time.Time, error) {
	if v == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", v)
	if err != nil {
		return nil, fmt.Errorf("invalid %s date %q, use YYYY-MM-DD", field, v)
	}
	return &t, nil
}
