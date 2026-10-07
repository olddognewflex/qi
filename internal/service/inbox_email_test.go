package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProposeInboxActionEmailTags(t *testing.T) {
	tests := []struct {
		line, action, reason string
	}{
		{"Email: Re: contract question — Pat Lee [reply_needed]", InboxActionTask, "email tag [reply_needed]"},
		{"Email: Expense reports due — Finance [flag_for_followup]", InboxActionTask, "email tag [flag_for_followup]"},
		{"Email: Sale ends tonight — Shop [read_now]", InboxActionArchive, "email tag [read_now]"},
		{"Email: Hours reminder —  [read_now]", InboxActionArchive, "email tag [read_now]"}, // empty sender
		{"Email: Weekly digest — News [mark_read]", InboxActionArchive, "email tag [mark_read]"},
		{"Email: Donate now — PAC [unsubscribe]", InboxActionArchive, "email tag [unsubscribe]"},
		{"Email: You won! — Spam [delete]", InboxActionArchive, "email tag [delete]"},
		{"Email: Re: question — Pat Lee [Reply_Needed]", InboxActionTask, "email tag [reply_needed]"}, // tag case folded
		{"Email: A — B — Sender [read_now]", InboxActionArchive, "email tag [read_now]"},              // dash in subject
		// Unknown tag and non-email lines fall through to the generic rules.
		{"Email: Odd one — Someone [mystery]", InboxActionTask, "short single-line capture reads as a task"},
		{"Emailed the landlord about the leak", InboxActionTask, "short single-line capture reads as a task"},
	}
	for _, tt := range tests {
		action, reason := proposeInboxAction([]string{tt.line})
		if action != tt.action || reason != tt.reason {
			t.Errorf("%q → %s (%s), want %s (%s)", tt.line, action, reason, tt.action, tt.reason)
		}
	}
	// A long email summary still uses its tag, not the long-line → note rule.
	long := "Email: " + strings.Repeat("very long subject ", 10) + "— Sender [read_now]"
	if action, _ := proposeInboxAction([]string{long}); action != InboxActionArchive {
		t.Errorf("long email capture → %s, want archive", action)
	}
	// A task marker still wins; a multi-line capture is not an email summary.
	if action, _ := proposeInboxAction([]string{"Email: x — y [read_now]", "- [ ] reply"}); action != InboxActionTask {
		t.Errorf("task marker should win")
	}
	if _, ok := emailCaptureTag([]string{"Email: x — y [read_now]", "more"}); ok {
		t.Errorf("multi-line body is not an email summary capture")
	}
}

func TestCollapseRepeats(t *testing.T) {
	items := []InboxItem{
		{Path: "a", Body: []string{"Email: PR merged — Bot [read_now]"}},
		{Path: "b", Body: []string{"buy milk"}},
		{Path: "c", Body: []string{"email:  PR merged — bot [read_now]"}}, // case + spacing
		{Path: "d", Body: nil},
		{Path: "e", Body: []string{"Email: PR merged — Bot [read_now]"}},
		{Path: "f", Body: []string{}},
		{Path: "g", Body: []string{"Email: PR merged — Other [read_now]"}}, // different sender
	}
	got := CollapseRepeats(items)
	want := []struct {
		path    string
		repeats []string
	}{
		{"a", []string{"c", "e"}},
		{"b", nil},
		{"d", []string{"f"}},
		{"g", nil},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d items, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Path != w.path || strings.Join(got[i].Repeats, ",") != strings.Join(w.repeats, ",") {
			t.Errorf("item %d = %s %v, want %s %v", i, got[i].Path, got[i].Repeats, w.path, w.repeats)
		}
	}
	if items[0].Repeats != nil {
		t.Errorf("input was modified")
	}
}

func TestApplyGroupTaskOnceArchivesRepeats(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "00-inbox")
	svc := newInboxService(t, inbox)
	line := "Email: Re: contract question — Pat Lee [reply_needed]"
	a := writeInboxCapture(t, inbox, "a.md", line)
	b := writeInboxCapture(t, inbox, "b.md", line)
	c := writeInboxCapture(t, inbox, "c.md", line)

	items, err := svc.List()
	if err != nil {
		t.Fatal(err)
	}
	groups := CollapseRepeats(items)
	if len(groups) != 1 || len(groups[0].Repeats) != 2 {
		t.Fatalf("groups = %+v", groups)
	}
	outs, err := svc.ApplyGroup(groups[0], InboxActionTask)
	if err != nil {
		t.Fatal(err)
	}
	if len(outs) != 3 || outs[0].Created == "" || outs[1].Created != "" || outs[2].Created != "" {
		t.Errorf("outs = %+v, want one created task then two plain archives", outs)
	}
	tasks, _ := os.ReadFile(filepath.Join(filepath.Dir(inbox), "inbox.md"))
	if n := strings.Count(string(tasks), "contract question"); n != 1 {
		t.Errorf("task file has %d copies, want 1:\n%s", n, tasks)
	}
	for _, p := range []string{a, b, c} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still in inbox", p)
		}
		if _, err := os.Stat(filepath.Join(inbox, "archive", filepath.Base(p))); err != nil {
			t.Errorf("%s not archived: %v", p, err)
		}
	}
}

func TestApplyGroupDeleteDeletesRepeats(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "00-inbox")
	svc := newInboxService(t, inbox)
	writeInboxCapture(t, inbox, "a.md", "spam")
	writeInboxCapture(t, inbox, "b.md", "spam")
	items, _ := svc.List()
	if _, err := svc.ApplyGroup(CollapseRepeats(items)[0], InboxActionDelete); err != nil {
		t.Fatal(err)
	}
	left, _ := os.ReadDir(inbox)
	if len(left) != 0 {
		t.Errorf("inbox not empty after delete: %v", left)
	}
}

func TestApplyGroupSkipsVanishedRepeat(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "00-inbox")
	svc := newInboxService(t, inbox)
	writeInboxCapture(t, inbox, "a.md", "spam")
	b := writeInboxCapture(t, inbox, "b.md", "spam")
	c := writeInboxCapture(t, inbox, "c.md", "spam")
	items, _ := svc.List()
	g := CollapseRepeats(items)[0]
	os.Remove(b) // vanished between listing and applying
	outs, err := svc.ApplyGroup(g, InboxActionArchive)
	if err != nil || len(outs) != 2 {
		t.Fatalf("outs=%v err=%v, want 2 applied and no error", outs, err)
	}
	if _, err := os.Stat(c); !os.IsNotExist(err) {
		t.Errorf("repeat after the vanished one was not applied")
	}
}

func TestApplyGroupRepeatErrorCreatesNothing(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "00-inbox")
	svc := newInboxService(t, inbox)
	a := writeInboxCapture(t, inbox, "a.md", "call the plumber")
	writeInboxCapture(t, inbox, "b.md", "call the plumber")
	items, _ := svc.List()
	g := CollapseRepeats(items)[0]
	g.Repeats = append(g.Repeats, filepath.Join(inbox, "sub", "x.md")) // outside the inbox: rejected
	_, err := svc.ApplyGroup(g, InboxActionTask)
	if err == nil {
		t.Fatal("want an error for the rejected repeat")
	}
	if _, err := os.Stat(svc.Tasks.TaskFilePath); !os.IsNotExist(err) {
		t.Errorf("no task may be created when a repeat could not be disposed of")
	}
	if _, err := os.Stat(a); err != nil {
		t.Errorf("representative must stay in the inbox to be re-triaged: %v", err)
	}
}

func TestApplyCreateFailureRestoresCapture(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "00-inbox")
	svc := newInboxService(t, inbox)
	blocker := filepath.Join(filepath.Dir(inbox), "blocker")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	svc.Tasks = TaskService{TaskFilePath: filepath.Join(blocker, "tasks", "inbox.md")} // parent is a file
	a := writeInboxCapture(t, inbox, "a.md", "call the plumber")

	if _, err := svc.Apply(InboxApplyInput{Path: a, Action: InboxActionTask}); err == nil {
		t.Fatal("want the task write to fail")
	}
	if _, err := os.Stat(a); err != nil {
		t.Errorf("capture must be restored to the inbox after a failed create: %v", err)
	}
	if left, _ := os.ReadDir(filepath.Join(inbox, "archive")); len(left) != 0 {
		t.Errorf("archive should be empty after restore: %v", left)
	}
}

func TestApplyGroupRepresentativeFailureAfterRepeats(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "00-inbox")
	svc := newInboxService(t, inbox)
	blocker := filepath.Join(filepath.Dir(inbox), "blocker")
	os.WriteFile(blocker, nil, 0o644)
	svc.Tasks = TaskService{TaskFilePath: filepath.Join(blocker, "tasks", "inbox.md")}
	a := writeInboxCapture(t, inbox, "a.md", "call the plumber")
	writeInboxCapture(t, inbox, "b.md", "call the plumber")
	items, _ := svc.List()
	outs, err := svc.ApplyGroup(CollapseRepeats(items)[0], InboxActionTask)
	if err == nil || len(outs) != 1 {
		t.Fatalf("outs=%v err=%v, want the archived repeat and an error", outs, err)
	}
	if _, err := os.Stat(a); err != nil {
		t.Errorf("representative must remain to be re-triaged: %v", err)
	}
}
