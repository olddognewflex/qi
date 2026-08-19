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
	if stats := fresh.Restore(entries); stats.Pending != 1 || stats.Terminal != 2 {
		t.Fatalf("restore stats = %+v, want 1 pending and 2 terminal", stats)
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
	if denied, ok := fresh.Get(deniedID); !ok || denied.Status != StatusDenied {
		t.Fatalf("denied entry = %+v, found=%v", denied, ok)
	}
	if done, ok := fresh.Get(doneID); !ok || done.Status != StatusExecuted {
		t.Fatalf("executed entry = %+v, found=%v", done, ok)
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
	if stats := fresh.Restore(entries); stats.Pending != 1 || stats.Terminal != 0 {
		t.Fatalf("restore stats = %+v, want 1 pending and 0 terminal", stats)
	}
	got, _ := fresh.Get(id)
	if got.Status != StatusPending {
		t.Fatalf("status = %s, want pending", got.Status)
	}
}

func TestQueueRestoreRejectsMismatchedTerminalBinding(t *testing.T) {
	enqueue := AuditEntry{
		Time: time.Now(), Event: EventEnqueue, ID: "approval-a",
		Caller: "ai-planner:session-a", CallID: "call-a", Tool: "task.add",
		Params: json.RawMessage(`{"text":"a"}`),
	}
	terminal := AuditEntry{
		Time: time.Now(), Event: EventExecute, ID: enqueue.ID,
		Caller: enqueue.Caller, CallID: enqueue.CallID, Tool: enqueue.Tool,
		ParamsHash: auditParamsHash(enqueue.Params),
		Outcome:    &TerminalOutcome{Status: StatusExecuted, Result: json.RawMessage(`{"id":"wrong"}`)},
	}
	tests := []struct {
		name   string
		mutate func(*AuditEntry)
	}{
		{name: "caller", mutate: func(e *AuditEntry) { e.Caller = "ai-planner:session-b" }},
		{name: "call id", mutate: func(e *AuditEntry) { e.CallID = "call-b" }},
		{name: "tool", mutate: func(e *AuditEntry) { e.Tool = "vault.capture" }},
		{name: "params", mutate: func(e *AuditEntry) { e.ParamsHash = auditParamsHash(json.RawMessage(`{"text":"b"}`)) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mismatched := terminal
			tt.mutate(&mismatched)
			fresh := NewQueue(nil)
			if stats := fresh.Restore([]AuditEntry{enqueue, mismatched}); stats != (RestoreStats{}) {
				t.Fatalf("restore stats = %+v, want no restored entry", stats)
			}
			if _, ok := fresh.Get(enqueue.ID); ok {
				t.Fatal("mismatched terminal outcome was restored")
			}
		})
	}
}

func TestAuditRejectsOversizedReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(MaxAuditLogBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAuditLog(path); err == nil || !strings.Contains(err.Error(), "audit log exceeds") {
		t.Fatalf("read error = %v, want total-size rejection", err)
	}
}

func TestAuditAppendRejectsTotalSizeLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	a, err := OpenAudit(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.f.Truncate(MaxAuditLogBytes); err != nil {
		t.Fatal(err)
	}
	if err := a.Append(AuditEntry{Event: EventEnqueue, ID: "overflow"}); err == nil || !strings.Contains(err.Error(), "audit log exceeds") {
		t.Fatalf("append error = %v, want total-size rejection", err)
	}
}

func TestQueueRestoreTerminalOutcomes(t *testing.T) {
	// Given: three approvals reach distinct terminal outcomes and are audited.
	q, path := mustQueue(t)
	executedID, err := q.EnqueueWithCallID("ai-planner:s1", "call-executed", "tool.execute", json.RawMessage(`{"value":1}`), "confirm")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Approve(executedID); err != nil {
		t.Fatal(err)
	}
	executedResult := json.RawMessage(`{"ok":true,"empty":null}`)
	if _, err := q.RecordResult(executedID, executedResult, nil); err != nil {
		t.Fatal(err)
	}

	deniedID, err := q.EnqueueWithCallID("ai-planner:s1", "call-denied", "tool.deny", json.RawMessage(`{"value":2}`), "confirm")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Deny(deniedID, "human refused"); err != nil {
		t.Fatal(err)
	}

	failedID, err := q.EnqueueWithCallID("ai-planner:s1", "call-failed", "tool.fail", json.RawMessage(`{"value":3}`), "confirm")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Approve(failedID); err != nil {
		t.Fatal(err)
	}
	if _, err := q.RecordResult(failedID, nil, errors.New("execution failed")); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadAuditLog(path)
	if err != nil {
		t.Fatal(err)
	}

	// When: a fresh queue restores only from durable audit records.
	fresh := NewQueue(nil)
	stats := fresh.Restore(entries)

	// Then: terminal entries remain lookup-only with their bound outcomes.
	if stats.Pending != 0 || stats.Terminal != 3 {
		t.Fatalf("restore stats = %+v, want 0 pending and 3 terminal", stats)
	}
	if got := fresh.List(""); len(got) != 0 {
		t.Fatalf("list returned %d terminal entries, want 0", len(got))
	}
	executed, ok := fresh.Get(executedID)
	if !ok || executed.Status != StatusExecuted || string(executed.Result) != string(executedResult) {
		t.Fatalf("executed = %+v, found=%v", executed, ok)
	}
	if executed.CallID != "call-executed" || executed.Caller != "ai-planner:s1" || executed.ToolName != "tool.execute" || string(executed.Params) != `{"value":1}` {
		t.Fatalf("executed binding = %+v", executed)
	}
	if executed.DecidedAt == nil || executed.ExecutedAt == nil {
		t.Fatalf("executed timestamps = %+v", executed)
	}
	denied, ok := fresh.Get(deniedID)
	if !ok || denied.Status != StatusDenied || denied.Reason != "human refused" || denied.DecidedAt == nil {
		t.Fatalf("denied = %+v, found=%v", denied, ok)
	}
	failed, ok := fresh.Get(failedID)
	if !ok || failed.Status != StatusFailed || failed.Err != "execution failed" || failed.ExecutedAt == nil {
		t.Fatalf("failed = %+v, found=%v", failed, ok)
	}
}

func TestQueueRestoreApprovedWithoutOutcomeStaysPending(t *testing.T) {
	// Given: approval was durable, but no terminal outcome was recorded.
	q, path := mustQueue(t)
	id, err := q.EnqueueWithCallID("ai-planner:s1", "call-1", "tool.mutate", json.RawMessage(`{"x":1}`), "confirm")
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

	// When: qid reconstructs the queue after an interruption.
	fresh := NewQueue(nil)
	stats := fresh.Restore(entries)

	// Then: the approval must be explicitly approved again with its binding intact.
	got, ok := fresh.Get(id)
	if stats.Pending != 1 || stats.Terminal != 0 || !ok {
		t.Fatalf("restore stats = %+v, found=%v", stats, ok)
	}
	if got.Status != StatusPending || got.CallID != "call-1" || string(got.Params) != `{"x":1}` {
		t.Fatalf("restored = %+v", got)
	}
}

func TestRecordResultSurfacesAuditFailure(t *testing.T) {
	// Given: a tool was approved, then its audit sink became unavailable.
	q, _ := mustQueue(t)
	id, err := q.Enqueue("ai", "tool.mutate", nil, "confirm")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Approve(id); err != nil {
		t.Fatal(err)
	}
	if err := q.audit.Close(); err != nil {
		t.Fatal(err)
	}

	// When: the executed result cannot be durably recorded.
	_, err = q.RecordResult(id, json.RawMessage(`{"ok":true}`), nil)

	// Then: the caller sees the ambiguity and published state remains approved.
	if err == nil || !strings.Contains(err.Error(), "tool may have executed; terminal result was not durably recorded") {
		t.Fatalf("err = %v", err)
	}
	got, _ := q.Get(id)
	if got.Status != StatusApproved || got.ExecutedAt != nil || len(got.Result) != 0 {
		t.Fatalf("entry published despite audit failure: %+v", got)
	}
}

func TestReadAuditRejectsMiddleCorruption(t *testing.T) {
	// Given: a malformed record appears before a later valid outcome.
	path := filepath.Join(t.TempDir(), "audit.log")
	data := "{\"event\":\"enqueue\",\"id\":\"a\"}\n{broken}\n{\"event\":\"deny\",\"id\":\"a\"}\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	// When/Then: replay fails instead of silently hiding later records.
	if _, err := ReadAuditLog(path); err == nil {
		t.Fatal("expected middle corruption error")
	}
}

func TestReadAuditRejectsOversizedMiddleRecord(t *testing.T) {
	// Given: an audit line exceeds the supported terminal payload envelope.
	path := filepath.Join(t.TempDir(), "audit.log")
	oversized := `{"event":"execute","id":"a","result":"` + strings.Repeat("x", (4<<20)+(128<<10)) + `"}`
	data := oversized + "\n" + `{"event":"enqueue","id":"b"}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	// When/Then: replay rejects the oversized middle record loudly.
	if _, err := ReadAuditLog(path); err == nil {
		t.Fatal("expected oversized record error")
	}
}

func TestEnqueueRollsBackOnAuditFailure(t *testing.T) {
	// Given: the audit sink is unavailable before enqueue.
	q, _ := mustQueue(t)
	if err := q.audit.Close(); err != nil {
		t.Fatal(err)
	}

	// When: a new approval is enqueued.
	id, err := q.Enqueue("ai", "tool.mutate", nil, "confirm")

	// Then: persistence fails and no in-memory entry is published.
	if err == nil {
		t.Fatal("expected enqueue audit error")
	}
	if id != "" || len(q.List("")) != 0 {
		t.Fatalf("id = %q, list = %+v", id, q.List(""))
	}
}

func TestRecordResultRejectsOversizedResult(t *testing.T) {
	// Given: an approved tool returns more than the durable result limit.
	q, _ := mustQueue(t)
	id, err := q.Enqueue("ai", "tool.mutate", nil, "confirm")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Approve(id); err != nil {
		t.Fatal(err)
	}
	result := json.RawMessage(`"` + strings.Repeat("x", (4<<20)+1) + `"`)

	// When: the queue attempts to persist the result.
	_, err = q.RecordResult(id, result, nil)

	// Then: it rejects the result without publishing a terminal state.
	if err == nil || !strings.Contains(err.Error(), "exceeds 4194304 bytes") {
		t.Fatalf("err = %v", err)
	}
	got, _ := q.Get(id)
	if got.Status != StatusApproved {
		t.Fatalf("status = %s, want approved", got.Status)
	}
}

func TestReadAuditAllowsTruncatedFinalRecord(t *testing.T) {
	// Given: the last audit write was interrupted before its newline.
	path := filepath.Join(t.TempDir(), "audit.log")
	data := "{\"event\":\"enqueue\",\"id\":\"a\"}\n{\"event\":\"execute\""
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	// When: the log is replayed.
	entries, err := ReadAuditLog(path)

	// Then: only the complete record is returned.
	if err != nil || len(entries) != 1 || entries[0].ID != "a" {
		t.Fatalf("entries = %+v, err = %v", entries, err)
	}
}
