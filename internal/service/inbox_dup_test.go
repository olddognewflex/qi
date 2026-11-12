package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestShortlistOpenTasks(t *testing.T) {
	tasks := []string{
		"renew passport",
		"call the dentist about the crown",
		"book dentist appointment",
		"buy milk",
		"book dentist appointment", // repeat: asked about once
		"   ",
	}
	cases := []struct {
		name string
		text string
		k    int
		want []string
	}{
		{"best first", "book the dentist", 5, []string{"book dentist appointment", "call the dentist about the crown"}},
		{"k caps", "book the dentist", 1, []string{"book dentist appointment"}},
		{"no shared word", "water the plants", 5, nil},
		{"stop words never match", "the and of to email re", 5, nil},
		{"case-insensitive", "RENEW Passport!", 5, []string{"renew passport"}},
		{"zero k", "renew passport", 0, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ShortlistOpenTasks(tc.text, tasks, tc.k)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ShortlistOpenTasks(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

func TestShortlistOpenTasks_TiesKeepTaskOrder(t *testing.T) {
	tasks := []string{"pay gas bill", "pay water bill", "pay phone bill"}
	got := ShortlistOpenTasks("pay bill", tasks, 2)
	if want := []string{"pay gas bill", "pay water bill"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// fakeDupChecker answers from probs keyed by "<joined body>|<candidate>" and
// records each call's checks. A missing key answers -1 (no answer).
type fakeDupChecker struct {
	mu      sync.Mutex
	probs   map[string]float64
	err     error
	short   bool // return one result too few
	calls   [][]InboxDupCheck
	checked []string
}

func (f *fakeDupChecker) CheckDuplicates(_ context.Context, checks []InboxDupCheck) ([][]float64, error) {
	f.mu.Lock()
	f.calls = append(f.calls, checks)
	for _, c := range checks {
		f.checked = append(f.checked, strings.Join(c.Body, "\n"))
	}
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float64, len(checks))
	for i, c := range checks {
		out[i] = make([]float64, len(c.Candidates))
		for k, cand := range c.Candidates {
			p, ok := f.probs[strings.Join(c.Body, "\n")+"|"+cand]
			if !ok {
				p = -1
			}
			out[i][k] = p
		}
	}
	if f.short {
		out = out[:len(out)-1]
	}
	return out, nil
}

func dupOpts(c InboxDuplicateChecker, tasks ...string) InboxRefineOptions {
	return InboxRefineOptions{MinConfidence: 0.5, Duplicates: c, OpenTasks: tasks, MinDuplicateProbability: 0.85}
}

func taskVerdict() InboxClassification {
	return InboxClassification{Action: InboxActionTask, Confidence: 0.9}
}

func TestInboxRefine_FlagsDuplicateAboveFloorOnly(t *testing.T) {
	items := []InboxItem{
		inboxItemFor("renew my passport"),
		inboxItemFor("book a dentist visit"),
	}
	cls := &fakeInboxClassifier{answers: map[string]InboxClassification{
		"renew my passport":    taskVerdict(),
		"book a dentist visit": taskVerdict(),
	}}
	dup := &fakeDupChecker{probs: map[string]float64{
		"renew my passport|renew passport":                0.93,
		"renew my passport|renew passport before holiday": 0.40,
		"book a dentist visit|book dentist appointment":   0.84, // just under the floor
	}}
	got, stats, err := InboxService{}.Refine(context.Background(), items, cls,
		dupOpts(dup, "renew passport", "renew passport before holiday", "book dentist appointment", "buy milk"))
	if err != nil {
		t.Fatalf("Refine: %v", err)
	}
	if got[0].DuplicateOf != "renew passport" || got[0].DuplicateProbability != 0.93 {
		t.Errorf("item 0 duplicate = %q %.2f, want renew passport 0.93", got[0].DuplicateOf, got[0].DuplicateProbability)
	}
	if got[1].DuplicateOf != "" {
		t.Errorf("item 1 flagged below the floor: %q", got[1].DuplicateOf)
	}
	for i, it := range got {
		if it.Action != InboxActionTask {
			t.Errorf("item %d Action = %q: a duplicate warning must never change the proposal", i, it.Action)
		}
	}
	if items[0].DuplicateOf != "" {
		t.Error("Refine modified its input")
	}
	if len(dup.calls) != 1 {
		t.Errorf("duplicate calls = %d, want 1 per batch", len(dup.calls))
	}
	if want := (InboxRefineStats{Refined: 2, DupChecked: 2, Duplicates: 1}); stats != want {
		t.Errorf("stats = %+v, want %+v", stats, want)
	}
}

func TestInboxRefine_DuplicateFloorBoundaryAndPartialAnswers(t *testing.T) {
	items := []InboxItem{
		inboxItemFor("renew my passport"),
		inboxItemFor("book a dentist visit"),
	}
	cls := &fakeInboxClassifier{answers: map[string]InboxClassification{
		"renew my passport":    taskVerdict(),
		"book a dentist visit": taskVerdict(),
	}}
	dup := &fakeDupChecker{probs: map[string]float64{
		// Exactly at the floor flags (>=).
		"renew my passport|renew passport": 0.85,
		// One candidate unanswered (-1), the other answered: the answered
		// one counts and the capture is not a failure.
		"book a dentist visit|dentist appointment booked": 0.9,
	}}
	got, stats, err := InboxService{}.Refine(context.Background(), items, cls,
		dupOpts(dup, "renew passport", "book dentist appointment", "dentist appointment booked"))
	if err != nil {
		t.Fatalf("Refine: %v", err)
	}
	if got[0].DuplicateOf != "renew passport" {
		t.Errorf("at-floor probability not flagged: %+v", got[0])
	}
	if got[1].DuplicateOf != "dentist appointment booked" || got[1].DuplicateProbability != 0.9 {
		t.Errorf("partial answer: %+v", got[1])
	}
	if stats.DupFailed != 0 || stats.Duplicates != 2 {
		t.Errorf("stats = %+v, want 2 flagged, none failed", stats)
	}
}

func TestInboxRefine_DuplicateCheckOnlyTaskProposalsWithCandidates(t *testing.T) {
	items := []InboxItem{
		inboxItemFor("renew my passport"),       // classified note: not checked
		inboxItemFor("water the plants"),        // task, but no shared word: not checked
		inboxItemFor("- [ ] renew my passport"), // authoritative: never leaves the machine
	}
	cls := &fakeInboxClassifier{answers: map[string]InboxClassification{
		"renew my passport": {Action: InboxActionNote, Confidence: 0.9},
		"water the plants":  taskVerdict(),
	}}
	dup := &fakeDupChecker{probs: map[string]float64{}}
	got, stats, err := InboxService{}.Refine(context.Background(), items, cls, dupOpts(dup, "renew passport"))
	if err != nil {
		t.Fatalf("Refine: %v", err)
	}
	if len(dup.calls) != 0 {
		t.Errorf("duplicate check called with %v; want no call when no task capture has candidates", dup.checked)
	}
	for i, it := range got {
		if it.DuplicateOf != "" {
			t.Errorf("item %d flagged: %q", i, it.DuplicateOf)
		}
	}
	if stats.DupChecked != 0 {
		t.Errorf("DupChecked = %d, want 0", stats.DupChecked)
	}
}

func TestInboxRefine_DuplicateCheckOneCallPerBatch(t *testing.T) {
	var items []InboxItem
	answers := map[string]InboxClassification{}
	for i := 0; i < InboxClassifyBatchSize+3; i++ {
		body := fmt.Sprintf("renew passport %d", i)
		items = append(items, inboxItemFor(body))
		answers[body] = taskVerdict()
	}
	cls := &fakeInboxClassifier{answers: answers}
	dup := &fakeDupChecker{probs: map[string]float64{}}
	_, stats, err := InboxService{}.Refine(context.Background(), items, cls, dupOpts(dup, "renew passport"))
	if err == nil || !strings.Contains(err.Error(), "duplicate check failed") {
		// Every answer is missing (-1): each capture is a per-capture failure.
		t.Fatalf("err = %v, want duplicate check failure for unanswered captures", err)
	}
	if len(dup.calls) != 2 {
		t.Errorf("duplicate calls = %d, want 2 (one per batch)", len(dup.calls))
	}
	if stats.DupChecked != len(items) || stats.DupFailed != len(items) {
		t.Errorf("stats = %+v, want all %d checked and failed", stats, len(items))
	}
}

func TestInboxRefine_DuplicateCheckErrorKeepsTriage(t *testing.T) {
	cases := []struct {
		name    string
		dup     *fakeDupChecker
		wantErr string
	}{
		{"batch error", &fakeDupChecker{err: errors.New("boom")}, "boom"},
		{"result count mismatch", &fakeDupChecker{probs: map[string]float64{"renew my passport|renew passport": 0.99}, short: true}, "returned 0 result(s) for 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items := []InboxItem{inboxItemFor("renew my passport")}
			cls := &fakeInboxClassifier{answers: map[string]InboxClassification{"renew my passport": taskVerdict()}}
			got, stats, err := InboxService{}.Refine(context.Background(), items, cls, dupOpts(tc.dup, "renew passport"))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !strings.Contains(err.Error(), "duplicate check failed for 1") {
				t.Fatalf("err = %v, want duplicate check failure containing %q", err, tc.wantErr)
			}
			if got[0].Action != InboxActionTask || got[0].DuplicateOf != "" {
				t.Errorf("item = %+v, want refined task with no warning", got[0])
			}
			if want := (InboxRefineStats{Refined: 1, DupChecked: 1, DupFailed: 1}); stats != want {
				t.Errorf("stats = %+v, want %+v", stats, want)
			}
		})
	}
}

func TestInboxRefine_NoDuplicateCheckAfterClassifyFailureOrWhenDisabled(t *testing.T) {
	items := []InboxItem{inboxItemFor("renew my passport")}
	failing := &fakeInboxClassifier{batchErrs: map[string]error{"renew my passport": errors.New("down")}}
	dup := &fakeDupChecker{probs: map[string]float64{"renew my passport|renew passport": 0.99}}
	if _, _, err := (InboxService{}).Refine(context.Background(), items, failing, dupOpts(dup, "renew passport")); err == nil {
		t.Fatal("want classifier error")
	}
	if len(dup.calls) != 0 {
		t.Errorf("duplicate check ran after the batch's classification failed")
	}

	ok := &fakeInboxClassifier{answers: map[string]InboxClassification{"renew my passport": taskVerdict()}}
	for name, opts := range map[string]InboxRefineOptions{
		"no checker":    dupOpts(nil, "renew passport"),
		"no open tasks": dupOpts(dup),
	} {
		got, _, err := InboxService{}.Refine(context.Background(), items, ok, opts)
		if err != nil || got[0].DuplicateOf != "" {
			t.Errorf("%s: err %v, duplicate %q; want no check", name, err, got[0].DuplicateOf)
		}
	}
	if len(dup.calls) != 0 {
		t.Errorf("duplicate check ran while disabled")
	}
}

func TestInboxRefine_DuplicateCheckUsesHeuristicTaskOnPerCaptureError(t *testing.T) {
	// A per-capture classifier error keeps the heuristic, which proposes a
	// task for a short line: it is still duplicate-checked.
	items := []InboxItem{inboxItemFor("renew my passport")}
	cls := &fakeInboxClassifier{errs: map[string]error{"renew my passport": errors.New("no answer")}}
	dup := &fakeDupChecker{probs: map[string]float64{"renew my passport|renew passport": 0.9}}
	got, stats, err := InboxService{}.Refine(context.Background(), items, cls, dupOpts(dup, "renew passport"))
	if err == nil {
		t.Fatal("want classifier error")
	}
	if got[0].DuplicateOf != "renew passport" {
		t.Errorf("DuplicateOf = %q, want renew passport", got[0].DuplicateOf)
	}
	if stats.Failed != 1 || stats.Duplicates != 1 {
		t.Errorf("stats = %+v", stats)
	}
}

func TestInboxPlanRefine_DuplicateCheck(t *testing.T) {
	items := []InboxItem{inboxItemFor("renew my passport")}
	if p := (InboxService{}).PlanRefine(items, dupOpts(&fakeDupChecker{}, "renew passport")); !p.DuplicateCheck {
		t.Errorf("plan %+v: want DuplicateCheck", p)
	}
	if p := (InboxService{}).PlanRefine(items, dupOpts(nil, "renew passport")); p.DuplicateCheck {
		t.Errorf("plan %+v: want no DuplicateCheck without a checker", p)
	}
	empty := []InboxItem{inboxItemFor()}
	if p := (InboxService{}).PlanRefine(empty, dupOpts(&fakeDupChecker{}, "renew passport")); p.DuplicateCheck {
		t.Errorf("plan %+v: want no DuplicateCheck when nothing is selected", p)
	}
}

func TestInboxRefine_DateAndDuplicateErrorsBothReported(t *testing.T) {
	items := []InboxItem{inboxItemFor("renew my passport")}
	cls := &fakeInboxClassifier{
		answers:  map[string]InboxClassification{"renew my passport": taskVerdict()},
		dateErrs: map[string]error{"renew my passport": errors.New("date down")},
	}
	dup := &fakeDupChecker{err: errors.New("dup down")}
	got, stats, err := InboxService{}.Refine(context.Background(), items, cls, dupOpts(dup, "renew passport"))
	if err == nil || !strings.Contains(err.Error(), "date lookup failed for 1") || !strings.Contains(err.Error(), "duplicate check failed for 1") {
		t.Fatalf("err = %v, want both the date and the duplicate failure", err)
	}
	if got[0].Action != InboxActionTask || stats.DateFailed != 1 || stats.DupFailed != 1 {
		t.Errorf("item %+v stats %+v", got[0], stats)
	}
}
