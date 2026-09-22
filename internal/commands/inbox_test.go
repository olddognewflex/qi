package commands

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"qi/internal/config"
	"qi/internal/service"
)

// stubInboxClassifier returns a fixed verdict per capture and counts the
// captures it was sent (calls) and the batch requests (batches).
type stubInboxClassifier struct {
	mu      sync.Mutex
	cls     service.InboxClassification
	calls   int
	batches int
}

func (s *stubInboxClassifier) ClassifyInbox(_ context.Context, bodies [][]string) ([]service.InboxVerdict, error) {
	s.mu.Lock()
	s.calls += len(bodies)
	s.batches++
	s.mu.Unlock()
	out := make([]service.InboxVerdict, len(bodies))
	for i := range out {
		out[i].InboxClassification = s.cls
	}
	return out, nil
}

// inboxTestConfig builds a config over a temp vault holding two captures: one
// the heuristic calls a task (short line) and one with an explicit marker.
func inboxTestConfig(t *testing.T) config.Config {
	t.Helper()
	vault := t.TempDir()
	inbox := filepath.Join(vault, "00-inbox")
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"01-idea.md":   "caching layer could reuse the index",
		"02-marker.md": "todo: renew passport",
	} {
		if err := os.WriteFile(filepath.Join(inbox, name), []byte("2026-06-11 09:00:00\n\n"+body+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return config.Config{
		VaultPath:    vault,
		InboxPath:    inbox,
		NotesPath:    filepath.Join(vault, "20-notes"),
		TaskFilePath: filepath.Join(vault, "10-tasks", "inbox.md"),
		TypeSafe:     config.TypeSafeConfig{APIKeyEnv: "QI_TEST_TYPESAFE_KEY"},
		Inbox:        config.InboxConfig{Classifier: config.InboxClassifierHeuristic, MinConfidence: 0.5, ClassifyLimit: config.DefaultInboxClassifyLimit},
	}
}

func stubInboxClassifierFor(t *testing.T, cls service.InboxClassification) *stubInboxClassifier {
	t.Helper()
	stub := &stubInboxClassifier{cls: cls}
	prev := buildInboxClassifier
	buildInboxClassifier = func(config.Config, string) service.InboxClassifier { return stub }
	t.Cleanup(func() { buildInboxClassifier = prev })
	return stub
}

func runInboxCmd(t *testing.T, cfg config.Config, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newInboxCommand(cfg)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

func TestInboxDryRunTypeSafeRefines(t *testing.T) {
	cfg := inboxTestConfig(t)
	t.Setenv("QI_TEST_TYPESAFE_KEY", "sk-test")
	stub := stubInboxClassifierFor(t, service.InboxClassification{Action: service.InboxActionNote, Confidence: 0.87})

	out, errOut, err := runInboxCmd(t, cfg, "--dry-run", "--classifier", "typesafe")
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	if !strings.Contains(out, "note    caching layer could reuse the index  (typesafe: note (confidence 0.87))") {
		t.Errorf("idea not refined:\n%s", out)
	}
	if !strings.Contains(out, "task    todo: renew passport  (contains a task marker)") {
		t.Errorf("marker capture should keep its heuristic:\n%s", out)
	}
	if stub.calls != 1 {
		t.Errorf("classifier calls = %d, want 1 (marker capture never sent)", stub.calls)
	}
	// Progress and summary go to stderr only; stdout above is the clean
	// dry-run listing.
	wantErr := "inbox: typesafe: classifying 1 capture(s) in 1 request(s)…\n" +
		"inbox: typesafe: refined 1, unsure 0, failed 0\n"
	if errOut != wantErr {
		t.Errorf("stderr = %q, want %q", errOut, wantErr)
	}
	if strings.Contains(out, "inbox: typesafe") {
		t.Errorf("progress leaked to stdout:\n%s", out)
	}
}

func TestInboxDryRunTypeSafeFromConfig(t *testing.T) {
	cfg := inboxTestConfig(t)
	cfg.Inbox.Classifier = config.InboxClassifierTypeSafe
	t.Setenv("QI_TEST_TYPESAFE_KEY", "sk-test")
	stub := stubInboxClassifierFor(t, service.InboxClassification{Action: service.InboxActionNote, Confidence: 0.9})

	if _, _, err := runInboxCmd(t, cfg, "--dry-run"); err != nil {
		t.Fatal(err)
	}
	if stub.calls != 1 {
		t.Errorf("config classifier not used: calls = %d", stub.calls)
	}

	// --classifier heuristic overrides the config back off.
	stub.calls = 0
	if _, _, err := runInboxCmd(t, cfg, "--dry-run", "--classifier", "heuristic"); err != nil {
		t.Fatal(err)
	}
	if stub.calls != 0 {
		t.Errorf("heuristic flag still called the classifier %d time(s)", stub.calls)
	}
}

func TestInboxDryRunTypeSafeMissingKeyFallsBack(t *testing.T) {
	cfg := inboxTestConfig(t)
	t.Setenv("QI_TEST_TYPESAFE_KEY", "")
	stub := stubInboxClassifierFor(t, service.InboxClassification{Action: service.InboxActionNote, Confidence: 0.9})

	out, errOut, err := runInboxCmd(t, cfg, "--dry-run", "--classifier", "typesafe")
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	if !strings.Contains(errOut, "inbox: QI_TEST_TYPESAFE_KEY unset; using heuristic proposals") {
		t.Errorf("missing-key warning absent: %q", errOut)
	}
	if !strings.Contains(out, "task    caching layer could reuse the index  (short single-line capture reads as a task)") {
		t.Errorf("heuristic proposal expected:\n%s", out)
	}
	if stub.calls != 0 {
		t.Errorf("classifier called %d time(s) without a key", stub.calls)
	}
}

func TestInboxRejectsUnknownClassifier(t *testing.T) {
	_, _, err := runInboxCmd(t, inboxTestConfig(t), "--dry-run", "--classifier", "llm")
	if err == nil || !strings.Contains(err.Error(), `unknown --classifier "llm"`) {
		t.Fatalf("err = %v", err)
	}
}

// writeCaptures adds n heuristic-guess captures to cfg's inbox.
func writeCaptures(t *testing.T, cfg config.Config, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		name := filepath.Join(cfg.InboxPath, fmt.Sprintf("10-extra-%03d.md", i))
		if err := os.WriteFile(name, []byte(fmt.Sprintf("2026-06-11 09:00:00\n\nextra thought %03d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInboxDryRunClassifyLimit(t *testing.T) {
	cfg := inboxTestConfig(t)
	writeCaptures(t, cfg, 29) // 30 eligible with the idea capture, plus the marker
	t.Setenv("QI_TEST_TYPESAFE_KEY", "sk-test")
	stub := stubInboxClassifierFor(t, service.InboxClassification{Action: service.InboxActionNote, Confidence: 0.4})

	out, errOut, err := runInboxCmd(t, cfg, "--dry-run", "--classifier", "typesafe", "--classify-limit", "26")
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	if stub.calls != 26 || stub.batches != 2 {
		t.Errorf("sent %d capture(s) in %d batch(es), want 26 in 2", stub.calls, stub.batches)
	}
	for _, want := range []string{
		"inbox: typesafe: classifying 26 capture(s) in 2 request(s)…",
		"inbox: typesafe: 4 more capture(s) over classify_limit 26 keep heuristic proposals",
		"inbox: typesafe: refined 0, unsure 26, failed 0",
	} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr missing %q:\n%s", want, errOut)
		}
	}
	if n := strings.Count(out, "; not classified (classify_limit)"); n != 4 {
		t.Errorf("%d capture(s) marked over the limit, want 4:\n%s", n, out)
	}

	// The config limit applies without the flag; --classify-limit 0 lifts it.
	cfg.Inbox.ClassifyLimit = 5
	stub.calls, stub.batches = 0, 0
	if _, _, err := runInboxCmd(t, cfg, "--dry-run", "--classifier", "typesafe"); err != nil {
		t.Fatal(err)
	}
	if stub.calls != 5 {
		t.Errorf("config limit: sent %d, want 5", stub.calls)
	}
	stub.calls, stub.batches = 0, 0
	if _, _, err := runInboxCmd(t, cfg, "--dry-run", "--classifier", "typesafe", "--classify-limit", "0"); err != nil {
		t.Fatal(err)
	}
	if stub.calls != 30 {
		t.Errorf("--classify-limit 0: sent %d, want 30", stub.calls)
	}
}

func TestInboxRejectsNegativeClassifyLimit(t *testing.T) {
	_, _, err := runInboxCmd(t, inboxTestConfig(t), "--dry-run", "--classify-limit", "-1")
	if err == nil || !strings.Contains(err.Error(), "--classify-limit -1 must be >= 0") {
		t.Fatalf("err = %v", err)
	}
}

func TestInboxClassifyBudget(t *testing.T) {
	cases := []struct {
		batches int
		want    time.Duration
	}{
		{0, 30 * time.Second},
		{1, 30 * time.Second},
		{4, 30 * time.Second},
		{5, 60 * time.Second},
		{12, 90 * time.Second},
		{100, 3 * time.Minute},
	}
	for _, tc := range cases {
		if got := inboxClassifyBudget(tc.batches); got != tc.want {
			t.Errorf("inboxClassifyBudget(%d) = %s, want %s", tc.batches, got, tc.want)
		}
	}
}
