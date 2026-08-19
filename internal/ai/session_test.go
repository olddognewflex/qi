package ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
)

const testSessionID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func mustSessionID(t *testing.T, raw string) SessionID {
	t.Helper()
	id, err := ParseSessionID(raw)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	return id
}

func testSession(t *testing.T) Session {
	t.Helper()
	return Session{
		Version:   SessionVersion,
		SessionID: mustSessionID(t, testSessionID),
		Model:     "claude-x",
		Provider: ProviderState{
			Version: ProviderStateVersion,
			Entries: []ProviderStateEntry{{Provider: ProviderAnthropic, Model: "claude-x", ConfigID: strings.Repeat("a", 64)}},
		},
		Messages: []Message{
			{Role: RoleUser, Text: "do the thing"},
			{Role: RoleAssistant, Text: "ok", ToolCalls: []ToolCall{
				{ID: "tu_0", Name: "ro_echo", Input: json.RawMessage(`{}`)},
				{ID: "tu_1", Name: "task_add", Input: json.RawMessage(`{"text":"x"}`)},
			}},
		},
		Results: []ToolResult{{CallID: "tu_0", Content: "prior ok"}},
		Pending: []PendingCall{{CallID: "tu_1", ApprovalID: "ap_9", ToolName: "task.add", Reason: "mutating"}},
	}
}

func newTestStore(t *testing.T) (*SessionStore, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "sessions")
	store, err := NewSessionStore(root)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return store, root
}

func TestGenerateSessionID(t *testing.T) {
	id, err := generateSessionID(bytes.NewReader(bytes.Repeat([]byte{0xab}, 32)))
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(id.String()) {
		t.Fatalf("id %q is not canonical 256-bit lowercase hex", id)
	}
}

func TestGenerateSessionIDRejectsReaderError(t *testing.T) {
	if _, err := generateSessionID(errorReader{}); err == nil {
		t.Fatal("expected random reader error")
	}
}

func TestGenerateSessionIDRejectsShortRead(t *testing.T) {
	if _, err := generateSessionID(bytes.NewReader(make([]byte, 31))); err == nil {
		t.Fatal("expected short random read error")
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("random source failed") }

func TestSessionRejectsInvalidID(t *testing.T) {
	invalid := []string{
		"", "abc", strings.Repeat("a", 63), strings.Repeat("a", 65),
		strings.Repeat("A", 64), strings.Repeat("g", 64), "../../outside",
		"/tmp/outside", `C:\\outside`, "a/b", `a\\b`, ".", "..",
	}
	for _, raw := range invalid {
		t.Run(fmt.Sprintf("%q", raw), func(t *testing.T) {
			if _, err := ParseSessionID(raw); err == nil {
				t.Fatalf("ParseSessionID(%q) succeeded", raw)
			}
		})
	}
}

func TestSessionRoundTrip(t *testing.T) {
	store, _ := newTestStore(t)
	sess := testSession(t)
	if err := store.Save(sess); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := store.Load(sess.SessionID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Version != SessionVersion || got.SessionID != sess.SessionID || got.Model != sess.Model {
		t.Fatalf("header mismatch: %+v", got)
	}
	if got.Results[0].Content != "prior ok" || got.Pending[0].ApprovalID != "ap_9" {
		t.Fatalf("body mismatch: %+v", got)
	}
	if err := store.Delete(sess.SessionID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := store.Delete(sess.SessionID); err != nil {
		t.Fatalf("idempotent delete: %v", err)
	}
	if _, err := store.Load(sess.SessionID); err == nil {
		t.Fatal("session should be gone after delete")
	}
}

func TestSessionModeRepair(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "sessions")
	if err := os.Mkdir(root, 0o777); err != nil {
		t.Fatal(err)
	}
	id := mustSessionID(t, testSessionID)
	data, err := json.Marshal(testSession(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, id.String()+".json"), data, 0o666); err != nil {
		t.Fatal(err)
	}
	store, err := NewSessionStore(root)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	if _, err := store.Load(id); err != nil {
		t.Fatalf("load: %v", err)
	}
	assertMode(t, root, 0o700)
	assertMode(t, filepath.Join(root, id.String()+".json"), 0o600)
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode %o, want %o", got, want)
	}
}

func TestSessionRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require privileges")
	}
	parent := t.TempDir()
	realRoot := filepath.Join(parent, "real")
	if err := os.Mkdir(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	rootLink := filepath.Join(parent, "link")
	if err := os.Symlink(realRoot, rootLink); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSessionStore(rootLink); err == nil {
		t.Fatal("expected symlink root rejection")
	}

	store, root := newTestStore(t)
	id := mustSessionID(t, testSessionID)
	sentinel := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(sentinel, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sentinel, filepath.Join(root, id.String()+".json")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(id); err == nil {
		t.Fatal("expected session symlink rejection")
	}
	if err := store.Delete(id); err == nil {
		t.Fatal("expected delete symlink rejection")
	}
	got, err := os.ReadFile(sentinel)
	if err != nil || string(got) != "unchanged" {
		t.Fatalf("outside sentinel changed: %q, %v", got, err)
	}
}

func TestSessionRejectsNonRegular(t *testing.T) {
	nonDirectoryRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(nonDirectoryRoot, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSessionStore(nonDirectoryRoot); err == nil {
		t.Fatal("expected non-directory root rejection")
	}

	store, root := newTestStore(t)
	id := mustSessionID(t, testSessionID)
	if err := os.Mkdir(filepath.Join(root, id.String()+".json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(id); err == nil {
		t.Fatal("expected non-regular session rejection")
	}
	if err := store.Delete(id); err == nil {
		t.Fatal("expected non-regular delete rejection")
	}
}

func TestSessionRejectsEmbeddedIDMismatch(t *testing.T) {
	store, root := newTestStore(t)
	sess := testSession(t)
	other := mustSessionID(t, strings.Repeat("f", 64))
	sess.SessionID = other
	writeRawSession(t, root, mustSessionID(t, testSessionID), sess)
	if _, err := store.Load(mustSessionID(t, testSessionID)); err == nil {
		t.Fatal("expected embedded id mismatch")
	}
}

func TestSessionRejectsUnknownVersion(t *testing.T) {
	store, root := newTestStore(t)
	sess := testSession(t)
	sess.Version = SessionVersion + 1
	writeRawSession(t, root, sess.SessionID, sess)
	if _, err := store.Load(sess.SessionID); err == nil {
		t.Fatal("expected unknown version rejection")
	}
	if err := store.Save(sess); err == nil {
		t.Fatal("expected unknown version save rejection")
	}
}

func TestSessionRejectsOversized(t *testing.T) {
	store, root := newTestStore(t)
	sess := testSession(t)
	if err := store.Save(sess); err != nil {
		t.Fatalf("save old snapshot: %v", err)
	}
	oldModel := sess.Model
	sess.Model = strings.Repeat("x", MaxSessionBytes)
	if err := store.Save(sess); err == nil {
		t.Fatal("expected oversized save rejection")
	}
	got, err := store.Load(sess.SessionID)
	if err != nil {
		t.Fatalf("load old snapshot after failed save: %v", err)
	}
	if got.Model != oldModel {
		t.Fatal("failed save replaced the previous snapshot")
	}
	path := filepath.Join(root, sess.SessionID.String()+".json")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), MaxSessionBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(sess.SessionID); err == nil {
		t.Fatal("expected oversized load rejection")
	}
}

func TestSessionRejectsTrailingJSON(t *testing.T) {
	store, root := newTestStore(t)
	sess := testSession(t)
	data, err := json.Marshal(sess)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, sess.SessionID.String()+".json"), append(data, []byte(" {}")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(sess.SessionID); err == nil {
		t.Fatal("expected trailing JSON rejection")
	}
}

func writeRawSession(t *testing.T, root string, id SessionID, sess Session) {
	t.Helper()
	data, err := json.Marshal(sess)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, id.String()+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSessionSaveAtomic(t *testing.T) {
	store, root := newTestStore(t)
	sess := testSession(t)
	sess.Model = strings.Repeat("old", 4096)
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	oldModel := sess.Model
	sess.Model = strings.Repeat("new", 4096)
	newModel := sess.Model

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 200 {
			got, err := store.Load(sess.SessionID)
			if err != nil {
				errCh <- err
				return
			}
			if got.Model != oldModel && got.Model != newModel {
				errCh <- fmt.Errorf("observed partial model of length %d", len(got.Model))
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		errCh <- store.Save(sess)
	}()
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("temporary file remains: %s", entry.Name())
		}
	}
}

func TestSessionSaveCleansTempOnRenameFailure(t *testing.T) {
	store, root := newTestStore(t)
	sess := testSession(t)
	if err := os.Mkdir(filepath.Join(root, sess.SessionID.String()+".json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(sess); err == nil {
		t.Fatal("expected rename failure")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("temporary file remains: %s", entry.Name())
		}
	}
}

func TestSessionLeaseIsNonblockingAcrossStores(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	a, err := NewSessionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Errorf("close first store: %v", err)
		}
	})
	b, err := NewSessionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := b.Close(); err != nil {
			t.Errorf("close second store: %v", err)
		}
	})
	id := mustSessionID(t, testSessionID)

	type result struct {
		lease *SessionLease
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, store := range []*SessionStore{a, b} {
		go func() {
			<-start
			lease, leaseErr := store.AcquireLease(id)
			results <- result{lease: lease, err: leaseErr}
		}()
	}
	close(start)
	first, second := <-results, <-results
	got := []result{first, second}
	var winner *SessionLease
	losers := 0
	for _, result := range got {
		switch {
		case result.err == nil:
			winner = result.lease
		case errors.Is(result.err, ErrSessionLeaseHeld):
			losers++
		default:
			t.Fatalf("unexpected lease error: %v", result.err)
		}
	}
	if winner == nil || losers != 1 {
		t.Fatalf("winner=%v losers=%d", winner != nil, losers)
	}
	assertMode(t, filepath.Join(root, id.String()+".lock"), 0o600)
	if err := winner.Release(); err != nil {
		t.Fatal(err)
	}
	later, err := b.AcquireLease(id)
	if err != nil {
		t.Fatalf("lease after release: %v", err)
	}
	if err := later.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionLeaseProcessExitReleasesLock(t *testing.T) {
	if os.Getenv("QI_SESSION_LEASE_HELPER") == "1" {
		store, err := NewSessionStore(os.Getenv("QI_SESSION_LEASE_ROOT"))
		if err != nil {
			os.Exit(2)
		}
		if _, err := store.AcquireLease(mustSessionID(t, testSessionID)); err != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}

	root := filepath.Join(t.TempDir(), "sessions")
	cmd := exec.Command(os.Args[0], "-test.run=^TestSessionLeaseProcessExitReleasesLock$")
	cmd.Env = append(os.Environ(), "QI_SESSION_LEASE_HELPER=1", "QI_SESSION_LEASE_ROOT="+root)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("lease holder subprocess: %v: %s", err, output)
	}
	store, err := NewSessionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	lease, err := store.AcquireLease(mustSessionID(t, testSessionID))
	if err != nil {
		t.Fatalf("acquire after holder process exit: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionLeaseRejectsSymlinkAndNonRegular(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require privileges")
	}
	store, root := newTestStore(t)
	id := mustSessionID(t, testSessionID)
	lockPath := filepath.Join(root, id.String()+".lock")
	sentinel := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(sentinel, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sentinel, lockPath); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireLease(id); err == nil {
		t.Fatal("expected symlink lock rejection")
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireLease(id); err == nil {
		t.Fatal("expected non-regular lock rejection")
	}
}

func TestSessionDirReturnsResolutionError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("home resolution differs on Windows")
	}
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "")
	if _, err := SessionDir(); err == nil {
		t.Fatal("expected home resolution error")
	}
}

func TestPlannerConstructorRejectsRandomFailure(t *testing.T) {
	stateRoot := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateRoot)
	if _, err := newPlannerWithReader(nil, nil, errorReader{}); err == nil {
		t.Fatal("expected constructor random error")
	}
	if _, err := newPlannerWithReader(nil, nil, io.LimitReader(bytes.NewReader(make([]byte, 32)), 31)); err == nil {
		t.Fatal("expected constructor short-read error")
	}
	sessionRoot, err := SessionDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sessionRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed constructor created session state: %v", err)
	}
}

func TestPlannerConstructorGeneratesCanonicalID(t *testing.T) {
	p, err := newPlannerWithReader(nil, nil, bytes.NewReader(bytes.Repeat([]byte{0xcd}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	want := CallerPrefix + strings.Repeat("cd", 32)
	if p.Caller() != want {
		t.Fatalf("caller = %q, want %q", p.Caller(), want)
	}
}
