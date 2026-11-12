package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCaptureTime(t *testing.T) {
	local := func(y int, m time.Month, d, hh, mm, ss int) time.Time {
		return time.Date(y, m, d, hh, mm, ss, 0, time.Local)
	}
	cases := []struct {
		name, file, header string
		want               time.Time
	}{
		{"filename", "2026-11-09-081500.md", "2026-01-01 00:00:00", local(2026, 11, 9, 8, 15, 0)},
		{"filename with collision suffix", "2026-11-09-081500-x7k2.md", "", local(2026, 11, 9, 8, 15, 0)},
		{"header fallback", "idea.md", "2026-06-11 09:00:00", local(2026, 6, 11, 9, 0, 0)},
		{"neither", "idea.md", "Email: hi", time.Time{}},
		{"impossible filename date falls back", "2026-02-30-081500.md", "2026-02-27 10:00:00", local(2026, 2, 27, 10, 0, 0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := captureTime(tc.file, tc.header); !got.Equal(tc.want) {
				t.Errorf("captureTime(%q, %q) = %v, want %v", tc.file, tc.header, got, tc.want)
			}
		})
	}
}

func TestInboxListSetsCaptured(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "00-inbox")
	writeInboxCapture(t, inbox, "2026-11-09-081500.md", "pay bill tomorrow")
	writeInboxCapture(t, inbox, "idea.md", "an idea")
	items, err := InboxService{InboxDir: inbox}.List()
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 11, 9, 8, 15, 0, 0, time.Local); !items[0].Captured.Equal(want) {
		t.Errorf("filename capture time = %v, want %v", items[0].Captured, want)
	}
	if want := time.Date(2026, 6, 11, 9, 0, 0, 0, time.Local); !items[1].Captured.Equal(want) {
		t.Errorf("header capture time = %v, want %v", items[1].Captured, want)
	}
	for _, it := range items {
		if it.Date != nil {
			t.Errorf("List must never propose a date: %+v", it)
		}
	}
}

func TestInboxRefineDates(t *testing.T) {
	tue := &InboxDate{Date: time.Date(2026, 11, 10, 0, 0, 0, 0, time.Local), Role: InboxDateDue, Confidence: 0.8}
	shaky := &InboxDate{Date: tue.Date, Role: InboxDateDue, Confidence: 0.49}
	atFloor := &InboxDate{Date: tue.Date, Role: InboxDateScheduled, Confidence: 0.5}
	noRole := &InboxDate{Date: tue.Date, Role: "", Confidence: 0.9}
	task := InboxClassification{Action: InboxActionTask, Confidence: 0.9}

	cases := []struct {
		name      string
		body      string
		cls       InboxClassification
		date      *InboxDate
		wantDate  *InboxDate
		wantDated int
	}{
		{"task with date", "pay bill tomorrow", task, tue, tue, 1},
		{"at the floor", "dentist friday", task, atFloor, atFloor, 1},
		{"below the floor", "call bank soonish", task, shaky, nil, 0},
		{"no date found", "buy milk", task, nil, nil, 0},
		{"unknown role dropped", "ship it tomorrow", task, noRole, nil, 0},
		{"note proposal gets no date", "great quote from the talk", InboxClassification{Action: InboxActionNote, Confidence: 0.9}, tue, nil, 0},
		{"archive proposal gets no date", "test test", InboxClassification{Action: InboxActionArchive, Confidence: 0.9}, tue, nil, 0},
		// Unsure verdict: the heuristic (short line → task) stands, and the
		// date read for the capture as a task is still proposed.
		{"unsure verdict keeps heuristic task", "renew domain tomorrow", InboxClassification{Action: InboxActionTask, Confidence: 0.3}, tue, tue, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeInboxClassifier{
				answers: map[string]InboxClassification{tc.body: tc.cls},
				dates:   map[string]*InboxDate{tc.body: tc.date},
			}
			items := []InboxItem{inboxItemFor(tc.body)}
			got, stats, err := InboxService{}.Refine(context.Background(), items, fake, InboxRefineOptions{MinConfidence: 0.5, MinDateConfidence: 0.5})
			if err != nil {
				t.Fatalf("refine: %v", err)
			}
			switch {
			case tc.wantDate == nil && got[0].Date != nil:
				t.Errorf("date = %+v, want none", *got[0].Date)
			case tc.wantDate != nil && (got[0].Date == nil || *got[0].Date != *tc.wantDate):
				t.Errorf("date = %v, want %+v", got[0].Date, *tc.wantDate)
			}
			if stats.Dated != tc.wantDated {
				t.Errorf("stats.Dated = %d, want %d", stats.Dated, tc.wantDated)
			}
			if got[0].Date != nil && got[0].Date == tc.date {
				t.Errorf("Refine must copy the date, not alias the classifier's")
			}
		})
	}
}

func TestInboxRefineSendsCaptureTime(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "00-inbox")
	writeInboxCapture(t, inbox, "2026-11-09-081500.md", "pay bill tomorrow")
	svc := InboxService{InboxDir: inbox}
	items, err := svc.List()
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeInboxClassifier{answers: map[string]InboxClassification{"pay bill tomorrow": {Action: InboxActionTask, Confidence: 0.9}}}
	if _, _, err := svc.Refine(context.Background(), items, fake, InboxRefineOptions{}); err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 11, 9, 8, 15, 0, 0, time.Local); !fake.captured["pay bill tomorrow"].Equal(want) {
		t.Errorf("classifier saw capture time %v, want %v (the capture's, not now)", fake.captured["pay bill tomorrow"], want)
	}
}

func TestInboxRefineDateErrLeavesTaskUndated(t *testing.T) {
	fake := &fakeInboxClassifier{
		answers:  map[string]InboxClassification{"pay bill tomorrow": {Action: InboxActionTask, Confidence: 0.9}},
		dateErrs: map[string]error{"pay bill tomorrow": errFake("date details: 503")},
	}
	got, stats, err := InboxService{}.Refine(context.Background(), []InboxItem{inboxItemFor("pay bill tomorrow")}, fake, InboxRefineOptions{MinConfidence: 0.5})
	if err == nil || !strings.Contains(err.Error(), "date lookup failed for 1 capture(s)") {
		t.Errorf("err = %v, want date lookup failure", err)
	}
	if got[0].Action != InboxActionTask || got[0].Date != nil {
		t.Errorf("item = %+v, want undated task", got[0])
	}
	if stats.Refined != 1 || stats.DateFailed != 1 || stats.Failed != 0 {
		t.Errorf("stats = %+v, want refined 1, date failed 1", stats)
	}
}

type errFake string

func (e errFake) Error() string { return string(e) }

func TestInboxApplyTaskWithDates(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "00-inbox")
	svc := newInboxService(t, inbox)
	due := time.Date(2026, 11, 10, 0, 0, 0, 0, time.Local)
	sched := time.Date(2026, 11, 12, 0, 0, 0, 0, time.Local)
	src := writeInboxCapture(t, inbox, "cap.md", "pay water bill")

	if _, err := svc.Apply(InboxApplyInput{Path: src, Action: InboxActionTask, Due: &due, Scheduled: &sched}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	body, err := os.ReadFile(svc.Tasks.TaskFilePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "pay water bill ⏳ 2026-11-12 📅 2026-11-10") {
		t.Errorf("task line missing dates: %s", body)
	}
}

func TestInboxApplyGroupDates(t *testing.T) {
	day := time.Date(2026, 11, 10, 0, 0, 0, 0, time.Local)
	cases := []struct {
		name, action, role, want, notWant string
	}{
		{"due", InboxActionTask, InboxDateDue, "pay bill 📅 2026-11-10", "⏳"},
		{"scheduled", InboxActionTask, InboxDateScheduled, "pay bill ⏳ 2026-11-10", "📅"},
		{"note ignores date", InboxActionNote, InboxDateDue, "", "2026-11-10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inbox := filepath.Join(t.TempDir(), "00-inbox")
			svc := newInboxService(t, inbox)
			src := writeInboxCapture(t, inbox, "a.md", "pay bill")
			rep := writeInboxCapture(t, inbox, "b.md", "pay bill")
			it := InboxItem{Path: src, Repeats: []string{rep}, Date: &InboxDate{Date: day, Role: tc.role, Confidence: 0.9}}
			if _, err := svc.ApplyGroup(it, tc.action); err != nil {
				t.Fatalf("apply group: %v", err)
			}
			tasks, _ := os.ReadFile(svc.Tasks.TaskFilePath)
			if tc.want != "" && !strings.Contains(string(tasks), tc.want) {
				t.Errorf("task file = %q, want %q", tasks, tc.want)
			}
			if n := strings.Count(string(tasks), "pay bill"); tc.action == InboxActionTask && n != 1 {
				t.Errorf("task file has %d tasks, want 1", n)
			}
			if strings.Contains(string(tasks), tc.notWant) {
				t.Errorf("task file = %q, must not contain %q", tasks, tc.notWant)
			}
			if tc.action == InboxActionNote {
				notes, _ := filepath.Glob(filepath.Join(filepath.Dir(inbox), "20-notes", "*.md"))
				for _, n := range notes {
					b, _ := os.ReadFile(n)
					if strings.Contains(string(b), "2026-11-10") {
						t.Errorf("note carries the date: %s", b)
					}
				}
			}
		})
	}
}

func TestCollapseRepeatsAnchorsToLatestCapture(t *testing.T) {
	mon := time.Date(2026, 11, 9, 8, 0, 0, 0, time.Local)
	thu := time.Date(2026, 11, 12, 8, 0, 0, 0, time.Local)
	items := []InboxItem{
		{Path: "a", Body: []string{"pay bill tomorrow"}, Captured: mon},
		{Path: "b", Body: []string{"Pay bill  tomorrow"}, Captured: thu},
		{Path: "c", Body: []string{"pay bill tomorrow"}, Captured: mon.Add(time.Hour)},
	}
	got := CollapseRepeats(items)
	if len(got) != 1 || got[0].Path != "a" {
		t.Fatalf("collapsed = %+v, want one group led by a", got)
	}
	if !got[0].Captured.Equal(thu) {
		t.Errorf("group capture time = %v, want the latest restatement %v", got[0].Captured, thu)
	}
	if !items[0].Captured.Equal(mon) {
		t.Errorf("input item mutated")
	}
}

func TestInboxRefineDateErrIgnoredForNonTask(t *testing.T) {
	// Model said task below the floor, heuristic says note (long line): the
	// date lookup failure is moot, so it is neither counted nor reported.
	body := "a long rambling thought about whether the caching layer could reuse the index someday"
	fake := &fakeInboxClassifier{
		answers:  map[string]InboxClassification{body: {Action: InboxActionTask, Confidence: 0.3}},
		dateErrs: map[string]error{body: errFake("date details: 503")},
	}
	got, stats, err := InboxService{}.Refine(context.Background(), []InboxItem{inboxItemFor(body)}, fake, InboxRefineOptions{MinConfidence: 0.5})
	if err != nil {
		t.Errorf("err = %v, want none", err)
	}
	if got[0].Action != InboxActionNote || stats.DateFailed != 0 {
		t.Errorf("action %s, stats %+v; want note and no date failure", got[0].Action, stats)
	}
}
