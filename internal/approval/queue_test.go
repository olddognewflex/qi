package approval

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mustQueue(t *testing.T) (*Queue, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.log")
	a, err := OpenAudit(path)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return NewQueue(a), path
}

func readAuditEvents(t *testing.T, path string) []AuditEntry {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open audit: %v", err)
	}
	defer f.Close()
	var out []AuditEntry
	s := bufio.NewScanner(f)
	for s.Scan() {
		var e AuditEntry
		if err := json.Unmarshal(s.Bytes(), &e); err != nil {
			t.Fatalf("parse audit line %q: %v", s.Text(), err)
		}
		out = append(out, e)
	}
	return out
}

func TestEnqueueGet(t *testing.T) {
	q, _ := mustQueue(t)
	id, err := q.Enqueue("ai", "vault.capture", json.RawMessage(`{"text":"hi"}`), "mutation by non-cli caller")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	p, ok := q.Get(id)
	if !ok {
		t.Fatal("get miss")
	}
	if p.Status != StatusPending {
		t.Fatalf("status = %s", p.Status)
	}
	if p.Caller != "ai" || p.ToolName != "vault.capture" {
		t.Fatalf("entry = %+v", p)
	}
}

func TestApproveExecuteCycle(t *testing.T) {
	q, auditPath := mustQueue(t)
	id, _ := q.Enqueue("ai", "vault.capture", json.RawMessage(`{"text":"hi"}`), "")

	if _, err := q.Approve(id); err != nil {
		t.Fatalf("approve: %v", err)
	}
	p, _ := q.Get(id)
	if p.Status != StatusApproved {
		t.Fatalf("status after approve = %s", p.Status)
	}
	if p.DecidedAt == nil {
		t.Fatal("decided_at not set")
	}

	res := json.RawMessage(`{"path":"/tmp/foo.md"}`)
	if _, err := q.RecordResult(id, res, nil); err != nil {
		t.Fatalf("record: %v", err)
	}
	p, _ = q.Get(id)
	if p.Status != StatusExecuted {
		t.Fatalf("status after exec = %s", p.Status)
	}
	if string(p.Result) != string(res) {
		t.Fatalf("result = %s", p.Result)
	}

	events := readAuditEvents(t, auditPath)
	wantEvents := []AuditEvent{EventEnqueue, EventApprove, EventExecute}
	if len(events) != len(wantEvents) {
		t.Fatalf("audit events = %d, want %d", len(events), len(wantEvents))
	}
	for i, want := range wantEvents {
		if events[i].Event != want {
			t.Errorf("[%d] event = %s, want %s", i, events[i].Event, want)
		}
	}
}

func TestDenyTransition(t *testing.T) {
	q, _ := mustQueue(t)
	id, _ := q.Enqueue("ai", "vault.capture", nil, "")
	if _, err := q.Deny(id, "looks suspicious"); err != nil {
		t.Fatalf("deny: %v", err)
	}
	p, _ := q.Get(id)
	if p.Status != StatusDenied {
		t.Fatalf("status = %s", p.Status)
	}
	if p.Reason != "looks suspicious" {
		t.Fatalf("reason = %q", p.Reason)
	}
}

func TestRejectDoubleDecide(t *testing.T) {
	q, _ := mustQueue(t)
	id, _ := q.Enqueue("ai", "vault.capture", nil, "")
	if _, err := q.Approve(id); err != nil {
		t.Fatalf("approve: %v", err)
	}
	_, err := q.Deny(id, "second decision")
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("err = %v, want ErrIllegalTransition", err)
	}
	_, err = q.Approve(id)
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("err = %v, want ErrIllegalTransition", err)
	}
}

func TestUnknownIDErrors(t *testing.T) {
	q, _ := mustQueue(t)
	_, err := q.Approve("ghost")
	if !errors.Is(err, ErrUnknownID) {
		t.Fatalf("err = %v, want ErrUnknownID", err)
	}
	_, err = q.Deny("ghost", "")
	if !errors.Is(err, ErrUnknownID) {
		t.Fatalf("err = %v, want ErrUnknownID", err)
	}
	_, err = q.RecordResult("ghost", nil, nil)
	if !errors.Is(err, ErrUnknownID) {
		t.Fatalf("err = %v, want ErrUnknownID", err)
	}
}

func TestRecordResultBeforeApproveRejected(t *testing.T) {
	q, _ := mustQueue(t)
	id, _ := q.Enqueue("ai", "vault.capture", nil, "")
	_, err := q.RecordResult(id, nil, nil)
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("err = %v, want ErrIllegalTransition", err)
	}
}

func TestFailExecution(t *testing.T) {
	q, auditPath := mustQueue(t)
	id, _ := q.Enqueue("ai", "vault.capture", nil, "")
	_, _ = q.Approve(id)
	if _, err := q.RecordResult(id, nil, errors.New("disk full")); err != nil {
		t.Fatalf("record: %v", err)
	}
	p, _ := q.Get(id)
	if p.Status != StatusFailed {
		t.Fatalf("status = %s", p.Status)
	}
	if p.Err != "disk full" {
		t.Fatalf("err = %q", p.Err)
	}
	events := readAuditEvents(t, auditPath)
	found := false
	for _, e := range events {
		if e.Event == EventFail && strings.Contains(e.Err, "disk full") {
			found = true
		}
	}
	if !found {
		t.Fatal("audit log missing fail entry")
	}
}

func TestListFilter(t *testing.T) {
	q, _ := mustQueue(t)
	a, _ := q.Enqueue("ai", "tool.a", nil, "")
	b, _ := q.Enqueue("ai", "tool.b", nil, "")
	_, _ = q.Deny(a, "no")

	pending := q.List(StatusPending)
	if len(pending) != 1 || pending[0].ID != b {
		t.Fatalf("pending = %+v", pending)
	}
	all := q.List("")
	if len(all) != 2 {
		t.Fatalf("all = %d, want 2", len(all))
	}
}

func TestListOrdered(t *testing.T) {
	q, _ := mustQueue(t)
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	q.now = func() time.Time { now = now.Add(time.Second); return now }
	a, _ := q.Enqueue("ai", "tool.a", nil, "")
	b, _ := q.Enqueue("ai", "tool.b", nil, "")
	c, _ := q.Enqueue("ai", "tool.c", nil, "")
	got := q.List("")
	if got[0].ID != a || got[1].ID != b || got[2].ID != c {
		t.Fatalf("order = %s,%s,%s", got[0].ID, got[1].ID, got[2].ID)
	}
}

func TestAuditFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "audit.log")
	a, err := OpenAudit(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer a.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %v, want 0o600", info.Mode().Perm())
	}
}

func TestQueueRestoreFromAudit(t *testing.T) {
	q, path := mustQueue(t)

	pendingID, err := q.Enqueue("ai-planner:s1", "capture", json.RawMessage(`{"text":"hi"}`), "needs approval")
	if err != nil {
		t.Fatal(err)
	}
	deniedID, err := q.Enqueue("mcp:s2", "task.add", nil, "r")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Deny(deniedID, "nope"); err != nil {
		t.Fatal(err)
	}
	doneID, err := q.Enqueue("ai-planner:s3", "note.write", nil, "r")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Approve(doneID); err != nil {
		t.Fatal(err)
	}
	if _, err := q.RecordResult(doneID, json.RawMessage(`{"ok":true}`), nil); err != nil {
		t.Fatal(err)
	}

	entries, err := ReadAuditLog(path)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}

	fresh := NewQueue(nil)
	if n := fresh.Restore(entries); n != 1 {
		t.Fatalf("restored %d entries, want 1", n)
	}

	got, ok := fresh.Get(pendingID)
	if !ok {
		t.Fatalf("pending entry %s not restored", pendingID)
	}
	if got.Status != StatusPending {
		t.Fatalf("status = %s, want pending", got.Status)
	}
	if got.Caller != "ai-planner:s1" || got.ToolName != "capture" {
		t.Fatalf("restored fields wrong: %+v", got)
	}
	if string(got.Params) != `{"text":"hi"}` {
		t.Fatalf("params = %s, want original", got.Params)
	}
	if _, ok := fresh.Get(deniedID); ok {
		t.Fatal("denied entry should not be restored")
	}
	if _, ok := fresh.Get(doneID); ok {
		t.Fatal("executed entry should not be restored")
	}
}

func TestQueueRestoreApprovedButNotExecuted(t *testing.T) {
	// An entry approved but with no execute/fail recorded models a crash
	// mid-execution. It must re-queue as pending (re-prompt the human) rather
	// than vanish.
	q, path := mustQueue(t)
	id, err := q.Enqueue("ai-planner:s1", "capture", nil, "r")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Approve(id); err != nil {
		t.Fatal(err)
	}

	entries, err := ReadAuditLog(path)
	if err != nil {
		t.Fatal(err)
	}
	fresh := NewQueue(nil)
	if n := fresh.Restore(entries); n != 1 {
		t.Fatalf("restored %d, want 1 (approved-but-unexecuted must re-queue)", n)
	}
	got, _ := fresh.Get(id)
	if got.Status != StatusPending {
		t.Fatalf("status = %s, want pending", got.Status)
	}
}
