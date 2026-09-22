package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qi/internal/config"
	"qi/internal/service"
)

// stubInboxClassifier returns a fixed verdict and counts calls.
type stubInboxClassifier struct {
	cls   service.InboxClassification
	calls int
}

func (s *stubInboxClassifier) ClassifyInbox(context.Context, []string) (service.InboxClassification, error) {
	s.calls++
	return s.cls, nil
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
		Inbox:        config.InboxConfig{Classifier: config.InboxClassifierHeuristic, MinConfidence: 0.5},
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
	if errOut != "" {
		t.Errorf("unexpected stderr: %q", errOut)
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
