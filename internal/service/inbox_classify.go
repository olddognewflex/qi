package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
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

// InboxCapture is one capture as a classifier sees it: its non-empty body
// lines and when it was written (zero if unknown), which relative dates in it
// are read against.
type InboxCapture struct {
	Body     []string
	Captured time.Time
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
	// Date is the date the classifier read for the capture as a task, already
	// resolved to a calendar day; nil when it found none or the date was
	// incomplete, impossible, or before the capture day (never a guess). It
	// may still be before today, when the capture is old. Refine applies it
	// only to task proposals at or above MinDateConfidence.
	Date *InboxDate
	// DateErr is set when looking up the date failed for this capture while
	// its action verdict stands; the capture is then proposed undated.
	DateErr error
	Err     error
}

// InboxDestination is a place a task can be routed: a configured project
// (Key is its #tag) or client (Key is its name, Client set). Description is
// what the classifier reads, so it should say what the project or client is.
type InboxDestination struct {
	Key         string
	Description string
	Client      bool
}

// InboxClassifier proposes triage actions for a batch of captures. It is
// the seam for the opt-in TypeSafe classifier (wired in commands); the service
// never constructs one itself, so List and the process-inbox skills stay
// deterministic and offline.
//
// dests are the task destinations to choose among; when empty the classifier
// asks no destination question (and leaves Destination empty). A non-nil
// error fails the whole batch (transport, API). Otherwise the verdicts must
// be index-aligned with caps — Refine treats a length mismatch as a batch
// failure — and a per-capture problem is that verdict's Err.
type InboxClassifier interface {
	ClassifyInbox(ctx context.Context, caps []InboxCapture, dests []InboxDestination) ([]InboxVerdict, error)
}

// InboxRefineOptions tunes Refine.
type InboxRefineOptions struct {
	// MinConfidence is the confidence a known action needs to override the
	// heuristic.
	MinConfidence float64
	// MinDateConfidence is the confidence a classifier-read date needs to be
	// proposed on a task.
	MinDateConfidence float64
	// Limit caps how many eligible captures are sent, in List order; <= 0
	// means no limit. The rest keep their heuristic proposal.
	Limit int
	// Destinations are the projects/clients a task may be routed to; empty
	// means no destination question is asked.
	Destinations []InboxDestination
	// MinDestinationConfidence is the confidence a destination needs to be
	// proposed; below it the item gets no destination at all.
	MinDestinationConfidence float64
	// Duplicates, when non-nil with OpenTasks, checks each classified
	// capture proposed as a task against a lexical shortlist of OpenTasks
	// (InboxDuplicateShortlist per capture): one extra call per batch, made
	// only when some capture in it has candidates.
	Duplicates InboxDuplicateChecker
	// OpenTasks are the open task texts duplicates are looked for among.
	OpenTasks []string
	// MinDuplicateProbability is the probability the most likely candidate
	// needs before the capture is flagged as its possible duplicate; 0 flags
	// any answered candidate, so callers set it explicitly.
	MinDuplicateProbability float64
}

// duplicateCheckEnabled reports whether Refine will run duplicate checks.
func (o InboxRefineOptions) duplicateCheckEnabled() bool {
	return o.Duplicates != nil && len(o.OpenTasks) > 0
}

// InboxRefinePlan says what Refine will send for a set of items, so a caller
// can size its timeout and report progress before any request goes out.
type InboxRefinePlan struct {
	Eligible int // captures whose heuristic is a guess
	Selected int // of those, sent to the classifier (after Limit)
	Skipped  int // eligible but over Limit: heuristic kept
	Batches  int // classifier calls, InboxClassifyBatchSize captures each
	// DuplicateCheck is true when each batch may make a second, duplicate
	// check call after classifying (so a batch can take two requests).
	DuplicateCheck bool
}

// InboxRefineStats tallies what Refine did with the Selected captures.
type InboxRefineStats struct {
	Refined int // classifier's action applied
	Unsure  int // verdict below MinConfidence or unknown action: heuristic kept
	Failed  int // classifier error (batch or per-capture): heuristic kept
	Routed  int // of the classified tasks, given a destination

	Dated      int // task proposals given a due/scheduled date
	DateFailed int // date lookup failed: proposed undated

	DupChecked int // task captures sent to the duplicate check
	Duplicates int // of those, flagged as a possible duplicate
	DupFailed  int // duplicate check error or no answer: no warning
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

		DuplicateCheck: opts.duplicateCheckEnabled() && len(selected) > 0,
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
//   - likewise, if the settled action is task and the verdict carries a date
//     with confidence >= MinDateConfidence, it becomes the item's Date. No
//     other item is dated: not notes or archives, not dates below the floor,
//     not captures the classifier never saw.
//
// With opts.Duplicates and opts.OpenTasks set, each batch then checks its
// captures now proposed as tasks against their lexical shortlist of open
// tasks, in one further call (none when no capture has a candidate), and sets
// DuplicateOf/DuplicateProbability on those whose most likely candidate
// reaches MinDuplicateProbability. That is only a warning: Action is never
// changed, so nothing is archived on the check's say-so. A batch whose
// classification failed is not duplicate-checked, and neither are captures
// the classifier never saw (explicit task markers, over Limit).
//
// A classifier error never fails triage: the affected items (a whole batch,
// or one capture) keep their heuristic proposal and one aggregated error is
// returned alongside the items so the caller can warn; a failed date lookup
// only leaves that task undated (and is reported the same way when nothing
// worse failed); a failed duplicate check only leaves those captures without
// a warning, and its error is joined to the returned one. items itself is not
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

	var shortlister taskShortlister
	if opts.duplicateCheckEnabled() {
		shortlister = newTaskShortlister(opts.OpenTasks)
	}

	var (
		wg          sync.WaitGroup
		mu          sync.Mutex
		firstErr    error
		dateErr     error
		firstDupErr error
		sem         = make(chan struct{}, InboxClassifyConcurrency)
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
			caps := make([]InboxCapture, len(batch))
			for k, i := range batch {
				caps[k] = InboxCapture{Body: out[i].Body, Captured: out[i].Captured}
			}
			verdicts, err := c.ClassifyInbox(ctx, caps, opts.Destinations)
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
				v := verdicts[k]
				applied := applyInboxClassification(&out[i], v.InboxClassification, opts.MinConfidence)
				routed := applyInboxDestination(&out[i], v, dests, opts.MinDestinationConfidence)
				dated := applyInboxDate(&out[i], v.Date, opts.MinDateConfidence)
				mu.Lock()
				if applied {
					stats.Refined++
				} else {
					stats.Unsure++
				}
				if routed {
					stats.Routed++
				}
				if dated {
					stats.Dated++
				}
				if v.DateErr != nil && out[i].Action == InboxActionTask { // a non-task never needed the date
					stats.DateFailed++
					if dateErr == nil {
						dateErr = v.DateErr
					}
				}
				mu.Unlock()
			}
			if opts.duplicateCheckEnabled() {
				n, flagged, failed, err := checkBatchDuplicates(ctx, out, batch, shortlister, opts)
				mu.Lock()
				stats.DupChecked += n
				stats.Duplicates += flagged
				stats.DupFailed += failed
				if failed > 0 && firstDupErr == nil {
					firstDupErr = err
				}
				mu.Unlock()
			}
		}(batch)
	}
	wg.Wait()

	var errs []error
	switch {
	case stats.Failed > 0:
		errs = append(errs, fmt.Errorf("classifier failed for %d capture(s), kept heuristic proposals: %w", stats.Failed, firstErr))
	case stats.DateFailed > 0:
		errs = append(errs, fmt.Errorf("date lookup failed for %d capture(s), proposed undated: %w", stats.DateFailed, dateErr))
	}
	if stats.DupFailed > 0 {
		errs = append(errs, fmt.Errorf("duplicate check failed for %d capture(s), no warning shown: %w", stats.DupFailed, firstDupErr))
	}
	return out, stats, errors.Join(errs...)
}

// checkBatchDuplicates runs the duplicate check over the batch's captures
// proposed as tasks that have shortlist candidates, in one call (none if no
// capture has one), and flags out[i] above the floor. It returns how many
// captures were checked, flagged and failed, with the first failure's error.
// Each goroutine writes only its own batch's items.
func checkBatchDuplicates(ctx context.Context, out []InboxItem, batch []int, sl taskShortlister, opts InboxRefineOptions) (checked, flagged, failed int, err error) {
	var (
		checks []InboxDupCheck
		idx    []int
	)
	for _, i := range batch {
		if out[i].Action != InboxActionTask {
			continue
		}
		cands := sl.shortlist(strings.Join(out[i].Body, "\n"), InboxDuplicateShortlist)
		if len(cands) == 0 {
			continue
		}
		checks = append(checks, InboxDupCheck{Body: out[i].Body, Candidates: cands})
		idx = append(idx, i)
	}
	if len(checks) == 0 {
		return 0, 0, 0, nil
	}
	results, err := opts.Duplicates.CheckDuplicates(ctx, checks)
	if err == nil && len(results) != len(checks) {
		err = fmt.Errorf("duplicate check returned %d result(s) for %d capture(s)", len(results), len(checks))
	}
	if err != nil {
		return len(checks), 0, len(checks), err
	}
	for k, i := range idx {
		if !answeredAny(results[k]) {
			failed++
			if err == nil {
				err = fmt.Errorf("duplicate check has no answer for %q", out[i].Summary)
			}
			continue
		}
		if applyDuplicate(&out[i], checks[k].Candidates, results[k], opts.MinDuplicateProbability) {
			flagged++
		}
	}
	return len(checks), flagged, failed, err
}

// applyInboxDate sets it.Date from a classifier date and reports whether it
// did: only for a task proposal, a known role, and confidence >=
// minConfidence. Otherwise it.Date is cleared, so an item never carries a date
// its final action cannot use.
func applyInboxDate(it *InboxItem, d *InboxDate, minConfidence float64) bool {
	it.Date = nil
	if d == nil || it.Action != InboxActionTask || d.Confidence < minConfidence ||
		(d.Role != InboxDateDue && d.Role != InboxDateScheduled) || d.Date.IsZero() {
		return false
	}
	date := *d
	it.Date = &date
	return true
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
