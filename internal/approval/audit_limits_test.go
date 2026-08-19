package approval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenAuditRepairsExistingFileMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	if err := os.WriteFile(path, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	audit, err := OpenAudit(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := audit.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestReadAuditLogRejectsEntryLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	data := strings.Repeat("{}\n", MaxAuditReplayEntries+1)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAuditLog(path); err == nil || !strings.Contains(err.Error(), "audit replay exceeds") {
		t.Fatalf("read error = %v, want entry-count rejection", err)
	}
}

func TestAuditAppendRejectsEntryLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	data := strings.Repeat("{}\n", MaxAuditReplayEntries)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	audit, err := OpenAudit(path)
	if err != nil {
		t.Fatal(err)
	}
	defer audit.Close()
	if err := audit.Append(AuditEntry{Event: EventEnqueue, ID: "overflow"}); err == nil || !strings.Contains(err.Error(), "audit replay exceeds") {
		t.Fatalf("append error = %v, want entry-count rejection", err)
	}
}

func TestAuditApproveReservesTerminalCapacity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	audit, err := OpenAudit(path)
	if err != nil {
		t.Fatal(err)
	}
	defer audit.Close()
	if err := audit.f.Truncate(MaxAuditLogBytes - MaxAuditRecordBytes); err != nil {
		t.Fatal(err)
	}
	entry := AuditEntry{Event: EventApprove, ID: "approval"}
	if err := audit.Append(entry); err == nil || !strings.Contains(err.Error(), "audit log exceeds") {
		t.Fatalf("approve error = %v, want reserved-capacity rejection", err)
	}
}

func TestAuditReservationSurvivesInterleavedWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	audit, err := OpenAudit(path)
	if err != nil {
		t.Fatal(err)
	}
	defer audit.Close()
	if err := audit.f.Truncate(MaxAuditLogBytes - MaxAuditRecordBytes - (32 << 10)); err != nil {
		t.Fatal(err)
	}
	if err := audit.Append(AuditEntry{Event: EventApprove, ID: "approval"}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	filler := AuditEntry{Event: EventEnqueue, ID: "filler", Params: json.RawMessage(`"` + strings.Repeat("x", 64<<10) + `"`)}
	if err := audit.Append(filler); err == nil || !strings.Contains(err.Error(), "audit log exceeds") {
		t.Fatalf("interleaved append error = %v, want reserved-capacity rejection", err)
	}
	terminal := AuditEntry{Event: EventExecute, ID: "approval", Outcome: &TerminalOutcome{Status: StatusExecuted, Result: json.RawMessage(`{}`)}}
	if err := audit.Append(terminal); err != nil {
		t.Fatalf("reserved terminal append: %v", err)
	}
	if err := audit.Append(filler); err != nil {
		t.Fatalf("append after reservation consumed: %v", err)
	}
}

func TestTerminalAuditStoresLargeResultWithoutDuplicatingParams(t *testing.T) {
	queue, path := mustQueue(t)
	params := json.RawMessage(`"` + strings.Repeat("p", 256<<10) + `"`)
	id, err := queue.EnqueueWithCallID("ai-planner:session", "call", "tool", params, "confirm")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Approve(id); err != nil {
		t.Fatal(err)
	}
	result := json.RawMessage(`"` + strings.Repeat("r", MaxTerminalResultBytes-2) + `"`)
	if _, err := queue.RecordResult(id, result, nil); err != nil {
		t.Fatal(err)
	}
	events, err := ReadAuditLog(path)
	if err != nil {
		t.Fatal(err)
	}
	terminal := events[len(events)-1]
	if len(terminal.Params) != 0 {
		t.Fatalf("terminal record duplicated %d parameter bytes", len(terminal.Params))
	}
	if terminal.ParamsHash != auditParamsHash(params) {
		t.Fatalf("params hash = %q, want enqueue binding", terminal.ParamsHash)
	}
}

func TestTerminalAuditFitsMaximumAcceptedMetadata(t *testing.T) {
	queue, _ := mustQueue(t)
	caller := strings.Repeat("\x01", MaxApprovalCallerBytes)
	callID := strings.Repeat("\x02", MaxApprovalCallIDBytes)
	tool := strings.Repeat("\x03", MaxApprovalToolBytes)
	id, err := queue.EnqueueWithCallID(caller, callID, tool, json.RawMessage(`{}`), "confirm")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Approve(id); err != nil {
		t.Fatal(err)
	}
	result := json.RawMessage(`"` + strings.Repeat("r", MaxTerminalResultBytes-2) + `"`)
	if _, err := queue.RecordResult(id, result, nil); err != nil {
		t.Fatalf("maximum accepted terminal envelope was not durable: %v", err)
	}
}

func TestQueueRejectsOversizedRepeatedMetadata(t *testing.T) {
	tests := []struct {
		name   string
		caller string
		callID string
		tool   string
	}{
		{name: "caller", caller: strings.Repeat("c", MaxApprovalCallerBytes+1), tool: "tool"},
		{name: "call id", caller: "caller", callID: strings.Repeat("i", MaxApprovalCallIDBytes+1), tool: "tool"},
		{name: "tool", caller: "caller", tool: strings.Repeat("t", MaxApprovalToolBytes+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queue := NewQueue(nil)
			if _, err := queue.EnqueueWithCallID(tt.caller, tt.callID, tt.tool, nil, ""); err == nil {
				t.Fatal("oversized metadata was accepted")
			}
		})
	}
}
