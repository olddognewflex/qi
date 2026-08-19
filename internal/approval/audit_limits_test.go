package approval

import (
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
