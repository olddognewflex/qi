package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// fakeInboxClassifier answers from a map keyed by the first body line and
// records every body it was asked about.
type fakeInboxClassifier struct {
	mu      sync.Mutex
	answers map[string]InboxClassification
	errs    map[string]error
	seen    []string
}

func (f *fakeInboxClassifier) ClassifyInbox(_ context.Context, body []string) (InboxClassification, error) {
	key := strings.Join(body, "\n")
	f.mu.Lock()
	f.seen = append(f.seen, key)
	f.mu.Unlock()
	if err := f.errs[key]; err != nil {
		return InboxClassification{}, err
	}
	return f.answers[key], nil
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
			got, err := InboxService{}.Refine(context.Background(), in, fake, 0.5)
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

	got, err := InboxService{}.Refine(context.Background(), items, fake, 0.5)
	if err == nil || !strings.Contains(err.Error(), "3 capture(s)") {
		t.Fatalf("err = %v, want one aggregated error for 3 failures", err)
	}
	if len(fake.seen) != len(items) {
		t.Errorf("classified %d, want %d", len(fake.seen), len(items))
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
	got, err := InboxService{}.Refine(context.Background(), in, nil, 0.5)
	if err != nil || got[0].Reason != in[0].Reason {
		t.Errorf("nil classifier should be a no-op: %+v, %v", got, err)
	}
}
