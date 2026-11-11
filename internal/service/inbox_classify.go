package service

import (
	"context"
	"fmt"
	"sync"
)

// InboxClassifyBatchSize is how many captures Refine hands the classifier per
// call — one TypeSafe request, which asks one self-contained question per
// capture over a shared state. It turns a 290-capture inbox into 12 requests
// instead of 290.
const InboxClassifyBatchSize = 25

// InboxClassifyConcurrency bounds how many batches are classified at once.
const InboxClassifyConcurrency = 4

// InboxClassification is an external classifier's verdict on one capture:
// the proposed action, its confidence in [0,1], and the full distribution.
type InboxClassification struct {
	Action        string
	Confidence    float64
	Probabilities map[string]float64
}

// InboxVerdict is one capture's outcome within a batch: a classification, or
// Err when the classifier had no usable answer for that capture alone.
// Destination is the answer to "if this were a task, where does it go?" — a
// key of the offered destinations, "none", or empty when none were offered —
// with its confidence; Refine uses it only for captures that end up as tasks.
type InboxVerdict struct {
	InboxClassification
	Destination           string
	DestinationConfidence float64
	Err                   error
}

// InboxDestination is a place a task can be routed: a configured project
// (Key is its #tag) or client (Key is its name, Client set). Description is
// what the classifier reads, so it should say what the project or client is.
type InboxDestination struct {
	Key         string
	Description string
	Client      bool
}

// InboxClassifier proposes triage actions for a batch of capture bodies. It is
// the seam for the opt-in TypeSafe classifier (wired in commands); the service
// never constructs one itself, so List and the process-inbox skills stay
// deterministic and offline.
//
// dests are the task destinations to choose among; when empty the classifier
// asks only the action question (and leaves Destination empty). A non-nil
// error fails the whole batch (transport, API). Otherwise the verdicts must
// be index-aligned with bodies — Refine treats a length mismatch as a batch
// failure — and a per-capture problem is that verdict's Err.
type InboxClassifier interface {
	ClassifyInbox(ctx context.Context, bodies [][]string, dests []InboxDestination) ([]InboxVerdict, error)
}

// InboxRefineOptions tunes Refine.
type InboxRefineOptions struct {
	// MinConfidence is the confidence a known action needs to override the
	// heuristic.
	MinConfidence float64
	// Limit caps how many eligible captures are sent, in List order; <= 0
	// means no limit. The rest keep their heuristic proposal.
	Limit int
	// Destinations are the projects/clients a task may be routed to; empty
	// means no destination question is asked.
	Destinations []InboxDestination
	// MinDestinationConfidence is the confidence a destination needs to be
	// proposed; below it the item gets no destination at all.
	MinDestinationConfidence float64
}

// InboxRefinePlan says what Refine will send for a set of items, so a caller
// can size its timeout and report progress before any request goes out.
type InboxRefinePlan struct {
	Eligible int // captures whose heuristic is a guess
	Selected int // of those, sent to the classifier (after Limit)
	Skipped  int // eligible but over Limit: heuristic kept
	Batches  int // classifier calls, InboxClassifyBatchSize captures each
}

// InboxRefineStats tallies what Refine did with the Selected captures.
type InboxRefineStats struct {
	Refined int // classifier's action applied
	Unsure  int // verdict below MinConfidence or unknown action: heuristic kept
	Failed  int // classifier error (batch or per-capture): heuristic kept
	Routed  int // of the classified tasks, given a destination
}

// limitNote is appended to the Reason of eligible captures over the limit.
const limitNote = "; not classified (classify_limit)"

// selectForRefine returns the indices of items the classifier should see, in
// order, and the indices it would have seen but for limit.
func selectForRefine(items []InboxItem, limit int) (selected, skipped []int) {
	for i := range items {
		if heuristicIsAuthoritative(items[i].Body) {
			continue
		}
		if limit > 0 && len(selected) >= limit {
			skipped = append(skipped, i)
			continue
		}
		selected = append(selected, i)
	}
	return selected, skipped
}

// PlanRefine reports what Refine(items, ..., opts) will send, without sending.
func (s InboxService) PlanRefine(items []InboxItem, opts InboxRefineOptions) InboxRefinePlan {
	selected, skipped := selectForRefine(items, opts.Limit)
	return InboxRefinePlan{
		Eligible: len(selected) + len(skipped),
		Selected: len(selected),
		Skipped:  len(skipped),
		Batches:  (len(selected) + InboxClassifyBatchSize - 1) / InboxClassifyBatchSize,
	}
}

// Refine re-proposes actions for items List produced, using c. Items whose
// heuristic is authoritative — an empty body (archive) or an explicit task
// marker — are left alone and never sent to c: code owns the rules it knows.
// Of the rest, the first opts.Limit (List order) are sent in batches of
// InboxClassifyBatchSize, InboxClassifyConcurrency batches at once, order
// preserved; any beyond the limit keep their heuristic with a Reason note.
// Per classified item:
//   - confidence >= MinConfidence and a known action (task/note/archive):
//     the classifier's action wins, Reason "typesafe: <action> (confidence 0.NN)";
//   - otherwise the heuristic action stands and Reason notes the unsure verdict.
//   - then, if the settled action is task and the verdict's destination is
//     one of opts.Destinations with confidence >= MinDestinationConfidence,
//     the item's Project (or Client) is set to it and Reason gains
//     "; → #<key> (0.NN)". Anything else — "none", an unknown key, a low
//     confidence, a non-task action — proposes no destination at all.
//
// A classifier error never fails triage: the affected items (a whole batch,
// or one capture) keep their heuristic proposal and one aggregated error is
// returned alongside the items so the caller can warn. items itself is not
// modified.
func (s InboxService) Refine(ctx context.Context, items []InboxItem, c InboxClassifier, opts InboxRefineOptions) ([]InboxItem, InboxRefineStats, error) {
	out := make([]InboxItem, len(items))
	copy(out, items)
	var stats InboxRefineStats
	if c == nil {
		return out, stats, nil
	}

	dests := make(map[string]InboxDestination, len(opts.Destinations))
	for _, d := range opts.Destinations {
		dests[d.Key] = d
	}

	selected, skipped := selectForRefine(out, opts.Limit)
	for _, i := range skipped {
		out[i].Reason += limitNote
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		sem      = make(chan struct{}, InboxClassifyConcurrency)
	)
	fail := func(n int, err error) {
		mu.Lock()
		defer mu.Unlock()
		stats.Failed += n
		if firstErr == nil {
			firstErr = err
		}
	}
	for start := 0; start < len(selected); start += InboxClassifyBatchSize {
		batch := selected[start:min(start+InboxClassifyBatchSize, len(selected))]
		wg.Add(1)
		sem <- struct{}{}
		go func(batch []int) {
			defer wg.Done()
			defer func() { <-sem }()
			bodies := make([][]string, len(batch))
			for k, i := range batch {
				bodies[k] = out[i].Body
			}
			verdicts, err := c.ClassifyInbox(ctx, bodies, opts.Destinations)
			if err == nil && len(verdicts) != len(batch) {
				err = fmt.Errorf("classifier returned %d verdict(s) for %d capture(s)", len(verdicts), len(batch))
			}
			if err != nil {
				fail(len(batch), err)
				return
			}
			for k, i := range batch {
				if verdicts[k].Err != nil {
					fail(1, verdicts[k].Err)
					continue
				}
				applied := applyInboxClassification(&out[i], verdicts[k].InboxClassification, opts.MinConfidence)
				routed := applyInboxDestination(&out[i], verdicts[k], dests, opts.MinDestinationConfidence)
				mu.Lock()
				if applied {
					stats.Refined++
				} else {
					stats.Unsure++
				}
				if routed {
					stats.Routed++
				}
				mu.Unlock()
			}
		}(batch)
	}
	wg.Wait()

	if stats.Failed > 0 {
		return out, stats, fmt.Errorf("classifier failed for %d capture(s), kept heuristic proposals: %w", stats.Failed, firstErr)
	}
	return out, stats, nil
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

// applyInboxClassification folds one classification into it and reports
// whether the classifier's action was applied (false: unsure, heuristic kept).
func applyInboxClassification(it *InboxItem, cls InboxClassification, minConfidence float64) bool {
	known := cls.Action == InboxActionTask || cls.Action == InboxActionNote || cls.Action == InboxActionArchive
	if known && cls.Confidence >= minConfidence {
		it.Action = cls.Action
		it.Reason = fmt.Sprintf("typesafe: %s (confidence %.2f)", cls.Action, cls.Confidence)
		return true
	}
	it.Reason = fmt.Sprintf("%s; typesafe unsure (%s %.2f)", it.Reason, cls.Action, cls.Confidence)
	return false
}

// applyInboxDestination proposes v's destination for it when it is settled as
// a task and the destination is a known, confident one, and reports whether it
// did. Below the floor it proposes nothing rather than a likely-wrong guess.
func applyInboxDestination(it *InboxItem, v InboxVerdict, dests map[string]InboxDestination, minConfidence float64) bool {
	if it.Action != InboxActionTask {
		return false
	}
	d, ok := dests[v.Destination]
	if !ok || v.DestinationConfidence < minConfidence {
		return false
	}
	if d.Client {
		it.Client = d.Key
	} else {
		it.Project = d.Key
	}
	it.Reason += fmt.Sprintf("; → #%s (%.2f)", d.Key, v.DestinationConfidence)
	return true
}
