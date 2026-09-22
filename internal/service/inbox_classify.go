package service

import (
	"context"
	"fmt"
	"sync"
)

// inboxClassifyConcurrency bounds how many captures are classified at once.
const inboxClassifyConcurrency = 4

// InboxClassification is an external classifier's verdict on one capture:
// the proposed action, its confidence in [0,1], and the full distribution.
type InboxClassification struct {
	Action        string
	Confidence    float64
	Probabilities map[string]float64
}

// InboxClassifier proposes a triage action for a capture body. It is the seam
// for the opt-in TypeSafe classifier (wired in commands); the service never
// constructs one itself, so List and the process-inbox skills stay
// deterministic and offline.
type InboxClassifier interface {
	ClassifyInbox(ctx context.Context, body []string) (InboxClassification, error)
}

// Refine re-proposes actions for items List produced, using c. Items whose
// heuristic is authoritative — an empty body (archive) or an explicit task
// marker — are left alone and never sent to c: code owns the rules it knows.
// The rest are classified with bounded concurrency, order preserved:
//   - confidence >= minConfidence and a known action (task/note/archive):
//     the classifier's action wins, Reason "typesafe: <action> (confidence 0.NN)";
//   - otherwise the heuristic action stands and Reason notes the unsure verdict.
//
// A classifier error never fails triage: that item keeps its heuristic
// proposal and one aggregated error is returned alongside the items so the
// caller can warn. items itself is not modified.
func (s InboxService) Refine(ctx context.Context, items []InboxItem, c InboxClassifier, minConfidence float64) ([]InboxItem, error) {
	out := make([]InboxItem, len(items))
	copy(out, items)
	if c == nil {
		return out, nil
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		failed   int
		firstErr error
		sem      = make(chan struct{}, inboxClassifyConcurrency)
	)
	for i := range out {
		if heuristicIsAuthoritative(out[i].Body) {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(it *InboxItem) {
			defer wg.Done()
			defer func() { <-sem }()
			cls, err := c.ClassifyInbox(ctx, it.Body)
			if err != nil {
				mu.Lock()
				failed++
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			applyInboxClassification(it, cls, minConfidence)
		}(&out[i])
	}
	wg.Wait()

	if failed > 0 {
		return out, fmt.Errorf("classifier failed for %d capture(s), kept heuristic proposals: %w", failed, firstErr)
	}
	return out, nil
}

// heuristicIsAuthoritative reports whether proposeInboxAction's verdict for
// body is a hard rule rather than a guess: nothing to action, or the user
// explicitly marked a task.
func heuristicIsAuthoritative(body []string) bool {
	if len(body) == 0 {
		return true
	}
	for _, line := range body {
		if hasTaskMarker(line) {
			return true
		}
	}
	return false
}

// applyInboxClassification folds one classification into it.
func applyInboxClassification(it *InboxItem, cls InboxClassification, minConfidence float64) {
	known := cls.Action == InboxActionTask || cls.Action == InboxActionNote || cls.Action == InboxActionArchive
	if known && cls.Confidence >= minConfidence {
		it.Action = cls.Action
		it.Reason = fmt.Sprintf("typesafe: %s (confidence %.2f)", cls.Action, cls.Confidence)
		return
	}
	it.Reason = fmt.Sprintf("%s; typesafe unsure (%s %.2f)", it.Reason, cls.Action, cls.Confidence)
}
