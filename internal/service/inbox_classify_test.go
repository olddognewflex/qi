package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// fakeInboxClassifier answers per capture from maps keyed by the joined body
// and records every body and batch it was asked about. A batch containing a
// key in batchErrs fails whole; a key in errs fails only that verdict.
type fakeInboxClassifier struct {
	mu        sync.Mutex
	answers   map[string]InboxClassification
	errs      map[string]error
	batchErrs map[string]error
	seen      []string
	batches   [][]string
}

func (f *fakeInboxClassifier) ClassifyInbox(_ context.Context, bodies [][]string) ([]InboxVerdict, error) {
	keys := make([]string, len(bodies))
	for i, body := range bodies {
		keys[i] = strings.Join(body, "\n")
	}
	f.mu.Lock()
	f.seen = append(f.seen, keys...)
	f.batches = append(f.batches, keys)
	f.mu.Unlock()
	for _, k := range keys {
		if err := f.batchErrs[k]; err != nil {
			return nil, err
		}
	}
	out := make([]InboxVerdict, len(keys))
	for i, k := range keys {
		out[i] = InboxVerdict{InboxClassification: f.answers[k], Err: f.errs[k]}
	}
	return out, nil
}

func inboxItemFor(body ...string) InboxItem {
	action, reason := proposeInboxAction(body)
	return InboxItem{Path: strings.Join(body, "/"), Summary: summarizeBody(body), Body: body, Action: action, Reason: reason}
}

func TestInboxRefine(t *testing.T) {
	heuristicShort := "short single-line capture reads as a task"
	heuristicLong := "multi-line or long content reads as a note"

	cases := []struct {
		name       string
		body       []string
		cls        InboxClassification
		err        error
		wantAction string
		wantReason string
		wantCalled bool
	}{
		{
			name:       "confident override",
			body:       []string{"interesting idea about caching"},
			cls:        InboxClassification{Action: InboxActionNote, Confidence: 0.83},
			wantAction: InboxActionNote,
			wantReason: "typesafe: note (confidence 0.83)",
			wantCalled: true,
		},
		{
			name:       "confident agreement",
			body:       []string{"call dentist"},
			cls:        InboxClassification{Action: InboxActionTask, Confidence: 0.5},
			wantAction: InboxActionTask,
			wantReason: "typesafe: task (confidence 0.50)",
			wantCalled: true,
		},
		{
			name:       "confident archive",
			body:       []string{"asdf", "test test"},
			cls:        InboxClassification{Action: InboxActionArchive, Confidence: 0.9},
			wantAction: InboxActionArchive,
			wantReason: "typesafe: archive (confidence 0.90)",
			wantCalled: true,
		},
		{
			name:       "below threshold keeps heuristic",
			body:       []string{"pick up the dry cleaning"},
			cls:        InboxClassification{Action: InboxActionNote, Confidence: 0.49},
			wantAction: InboxActionTask,
			wantReason: heuristicShort + "; typesafe unsure (note 0.49)",
			wantCalled: true,
		},
		{
			name:       "unknown action keeps heuristic",
			body:       []string{"line one", "line two"},
			cls:        InboxClassification{Action: InboxActionDelete, Confidence: 0.99},
			wantAction: InboxActionNote,
			wantReason: heuristicLong + "; typesafe unsure (delete 0.99)",
			wantCalled: true,
		},
		{
			name:       "classifier error keeps heuristic",
			body:       []string{"flaky"},
			err:        errors.New("boom"),
			wantAction: InboxActionTask,
			wantReason: heuristicShort,
			wantCalled: true,
		},
		{
			name:       "task marker is authoritative",
			body:       []string{"some context", "todo: send invoice"},
			cls:        InboxClassification{Action: InboxActionNote, Confidence: 1},
			wantAction: InboxActionTask,
			wantReason: "contains a task marker",
		},
		{
			name:       "empty body is authoritative",
			body:       nil,
			cls:        InboxClassification{Action: InboxActionNote, Confidence: 1},
			wantAction: InboxActionArchive,
			wantReason: "empty capture — nothing to action",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := strings.Join(tc.body, "\n")
			fake := &fakeInboxClassifier{
				answers: map[string]InboxClassification{key: tc.cls},
				errs:    map[string]error{key: tc.err},
			}
			in := []InboxItem{inboxItemFor(tc.body...)}
			got, _, err := InboxService{}.Refine(context.Background(), in, fake, InboxRefineOptions{MinConfidence: 0.5})
			if (err != nil) != (tc.err != nil) {
				t.Fatalf("err = %v, want error %v", err, tc.err != nil)
			}
			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Errorf("aggregated error %v does not wrap %v", err, tc.err)
			}
			if called := len(fake.seen) > 0; called != tc.wantCalled {
				t.Errorf("classifier called = %v, want %v", called, tc.wantCalled)
			}
			if got[0].Action != tc.wantAction || got[0].Reason != tc.wantReason {
				t.Errorf("got %s / %q, want %s / %q", got[0].Action, got[0].Reason, tc.wantAction, tc.wantReason)
			}
			if in[0].Reason != inboxItemFor(tc.body...).Reason {
				t.Errorf("input item mutated: %+v", in[0])
			}
		})
	}
}

func TestInboxRefinePreservesOrderAndAggregatesErrors(t *testing.T) {
	var items []InboxItem
	answers := map[string]InboxClassification{}
	errs := map[string]error{}
	for i := 0; i < 12; i++ {
		body := string(rune('a'+i)) + " capture"
		items = append(items, inboxItemFor(body))
		answers[body] = InboxClassification{Action: InboxActionNote, Confidence: 0.9}
		if i%5 == 0 {
			errs[body] = errors.New("rate limited")
		}
	}
	fake := &fakeInboxClassifier{answers: answers, errs: errs}

	got, stats, err := InboxService{}.Refine(context.Background(), items, fake, InboxRefineOptions{MinConfidence: 0.5})
	if err == nil || !strings.Contains(err.Error(), "3 capture(s)") {
		t.Fatalf("err = %v, want one aggregated error for 3 failures", err)
	}
	if len(fake.seen) != len(items) {
		t.Errorf("classified %d, want %d", len(fake.seen), len(items))
	}
	if want := (InboxRefineStats{Refined: 9, Failed: 3}); stats != want {
		t.Errorf("stats = %+v, want %+v", stats, want)
	}
	for i, it := range got {
		if it.Path != items[i].Path {
			t.Fatalf("order changed at %d: %s != %s", i, it.Path, items[i].Path)
		}
		want := InboxActionNote
		if i%5 == 0 {
			want = InboxActionTask // failed: heuristic kept
		}
		if it.Action != want {
			t.Errorf("item %d action = %s, want %s", i, it.Action, want)
		}
	}
}

func TestInboxRefineNilClassifier(t *testing.T) {
	in := []InboxItem{inboxItemFor("call dentist")}
	got, stats, err := InboxService{}.Refine(context.Background(), in, nil, InboxRefineOptions{MinConfidence: 0.5})
	if err != nil || got[0].Reason != in[0].Reason || stats != (InboxRefineStats{}) {
		t.Errorf("nil classifier should be a no-op: %+v, %v", got, err)
	}
}

// numberedItems returns n heuristic-guess captures ("capture 000", ...) and
// a fake answering note for each.
func numberedItems(n int) ([]InboxItem, *fakeInboxClassifier) {
	fake := &fakeInboxClassifier{answers: map[string]InboxClassification{}, errs: map[string]error{}, batchErrs: map[string]error{}}
	items := make([]InboxItem, n)
	for i := range items {
		body := fmt.Sprintf("capture %03d", i)
		items[i] = inboxItemFor(body)
		fake.answers[body] = InboxClassification{Action: InboxActionNote, Confidence: 0.9}
	}
	return items, fake
}

func TestInboxRefineBatches(t *testing.T) {
	items, fake := numberedItems(60)
	// An authoritative capture in the middle is never batched.
	items = append(items[:30], append([]InboxItem{inboxItemFor("todo: file taxes")}, items[30:]...)...)

	plan := InboxService{}.PlanRefine(items, InboxRefineOptions{})
	if want := (InboxRefinePlan{Eligible: 60, Selected: 60, Batches: 3}); plan != want {
		t.Errorf("plan = %+v, want %+v", plan, want)
	}

	got, stats, err := InboxService{}.Refine(context.Background(), items, fake, InboxRefineOptions{MinConfidence: 0.5})
	if err != nil {
		t.Fatalf("Refine: %v", err)
	}
	if stats != (InboxRefineStats{Refined: 60}) {
		t.Errorf("stats = %+v", stats)
	}
	sizes := map[int]int{}
	for _, b := range fake.batches {
		sizes[len(b)]++
		for k := 1; k < len(b); k++ {
			if b[k-1] >= b[k] {
				t.Errorf("batch not in List order: %v", b)
			}
		}
	}
	if len(fake.batches) != 3 || sizes[25] != 2 || sizes[10] != 1 {
		t.Errorf("batch sizes = %v over %d batches, want 25/25/10", sizes, len(fake.batches))
	}
	for i, it := range got {
		if it.Path != items[i].Path {
			t.Fatalf("order changed at %d: %s != %s", i, it.Path, items[i].Path)
		}
		want := InboxActionNote
		if i == 30 {
			want = InboxActionTask // authoritative marker untouched
		}
		if it.Action != want {
			t.Errorf("item %d action = %s, want %s", i, it.Action, want)
		}
	}
}

func TestInboxRefineLimit(t *testing.T) {
	items, fake := numberedItems(10)
	items = append([]InboxItem{inboxItemFor("todo: file taxes")}, items...)
	opts := InboxRefineOptions{MinConfidence: 0.5, Limit: 4}

	plan := InboxService{}.PlanRefine(items, opts)
	if want := (InboxRefinePlan{Eligible: 10, Selected: 4, Skipped: 6, Batches: 1}); plan != want {
		t.Errorf("plan = %+v, want %+v", plan, want)
	}

	got, stats, err := InboxService{}.Refine(context.Background(), items, fake, opts)
	if err != nil {
		t.Fatalf("Refine: %v", err)
	}
	if stats != (InboxRefineStats{Refined: 4}) || len(fake.seen) != 4 {
		t.Errorf("stats = %+v, seen = %v", stats, fake.seen)
	}
	// The first 4 eligible in List order were sent (the marker does not count).
	for k, key := range fake.seen {
		if want := fmt.Sprintf("capture %03d", k); key != want {
			t.Errorf("sent[%d] = %q, want %q", k, key, want)
		}
	}
	if got[0].Reason != "contains a task marker" {
		t.Errorf("authoritative item annotated: %q", got[0].Reason)
	}
	for i := 5; i < len(got); i++ {
		want := "short single-line capture reads as a task; not classified (classify_limit)"
		if got[i].Action != InboxActionTask || got[i].Reason != want {
			t.Errorf("item %d = %s / %q, want task / %q", i, got[i].Action, got[i].Reason, want)
		}
	}
	if items[5].Reason != "short single-line capture reads as a task" {
		t.Errorf("input item mutated: %q", items[5].Reason)
	}

	// Limit 0 means no limit.
	if p := (InboxService{}).PlanRefine(items, InboxRefineOptions{}); p.Selected != 10 || p.Skipped != 0 {
		t.Errorf("limit 0 plan = %+v", p)
	}
}

func TestInboxRefineBatchFailureFallsBack(t *testing.T) {
	items, fake := numberedItems(30)
	boom := errors.New("529 overloaded")
	fake.batchErrs["capture 026"] = boom // second batch (25..29) fails whole
	fake.errs["capture 003"] = errors.New("no answer")
	fake.answers["capture 004"] = InboxClassification{Action: InboxActionNote, Confidence: 0.2}

	got, stats, err := InboxService{}.Refine(context.Background(), items, fake, InboxRefineOptions{MinConfidence: 0.5})
	if err == nil || !strings.Contains(err.Error(), "6 capture(s)") {
		t.Fatalf("err = %v, want aggregated error for 6 failures", err)
	}
	if want := (InboxRefineStats{Refined: 23, Unsure: 1, Failed: 6}); stats != want {
		t.Errorf("stats = %+v, want %+v", stats, want)
	}
	for i, it := range got {
		switch {
		case i >= 25 || i == 3:
			if it.Action != InboxActionTask || it.Reason != items[i].Reason {
				t.Errorf("failed item %d should keep heuristic: %s / %q", i, it.Action, it.Reason)
			}
		case i == 4:
			if it.Action != InboxActionTask || !strings.Contains(it.Reason, "typesafe unsure (note 0.20)") {
				t.Errorf("unsure item = %s / %q", it.Action, it.Reason)
			}
		default:
			if it.Action != InboxActionNote {
				t.Errorf("item %d action = %s, want note", i, it.Action)
			}
		}
	}
}

// shortClassifier returns fewer verdicts than bodies.
type shortClassifier struct{}

func (shortClassifier) ClassifyInbox(_ context.Context, bodies [][]string) ([]InboxVerdict, error) {
	return make([]InboxVerdict, len(bodies)-1), nil
}

func TestInboxRefineVerdictCountMismatchFailsBatch(t *testing.T) {
	items, _ := numberedItems(3)
	got, stats, err := InboxService{}.Refine(context.Background(), items, shortClassifier{}, InboxRefineOptions{MinConfidence: 0.5})
	if err == nil || !strings.Contains(err.Error(), "2 verdict(s) for 3 capture(s)") {
		t.Fatalf("err = %v", err)
	}
	if stats.Failed != 3 || got[0].Action != InboxActionTask {
		t.Errorf("stats = %+v, item 0 = %+v", stats, got[0])
	}
}
