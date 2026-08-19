package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qi/internal/ai"
	"qi/internal/approval"
	"qi/internal/config"
	"qi/internal/daemon"
	"qi/internal/policy"
	"qi/internal/tools"
)

type deleteErrorStore struct {
	*ai.SessionStore
	err error
}

func (s *deleteErrorStore) Delete(ai.SessionID) error { return s.err }

func TestAIResumeCLIExecutedOutcomeAfterRestore(t *testing.T) {
	// Given
	fixture := newResumeCLIFixture(t)
	var output bytes.Buffer
	command := newAIResumeCommand()
	command.SetOut(&output)
	command.SetArgs([]string{fixture.sessionID.String(), "--socket", fixture.socket})

	// When
	err := command.Execute()
	// Then
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "resumed") {
		t.Fatalf("output = %q", output.String())
	}
	store, openErr := ai.DefaultSessionStore()
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer store.Close()
	if _, loadErr := store.Load(fixture.sessionID); loadErr == nil {
		t.Fatal("completed session was not deleted")
	}
	sessionDir, dirErr := ai.SessionDir()
	if dirErr != nil {
		t.Fatal(dirErr)
	}
	lockPath := filepath.Join(sessionDir, fixture.sessionID.String()+".lock")
	if _, statErr := os.Stat(lockPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("completed session lease was not deleted: %v", statErr)
	}
}

func TestAIResumeCLIDenyAndFail(t *testing.T) {
	for _, outcome := range []string{"denied", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			// Given
			fixture := newResumeCLIFixtureOutcome(t, outcome)
			var output bytes.Buffer
			command := newAIResumeCommand()
			command.SetOut(&output)
			command.SetArgs([]string{fixture.sessionID.String(), "--socket", fixture.socket})

			// When
			err := command.Execute()
			// Then
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), "resumed") {
				t.Fatalf("output = %q", output.String())
			}
		})
	}
}

func TestAIResumeCLIKeepsSessionAtSecondGate(t *testing.T) {
	// Given
	fixture := newResumeCLIFixtureOutcome(t, "second_gate")
	var output bytes.Buffer
	command := newAIResumeCommand()
	command.SetOut(&output)
	command.SetArgs([]string{fixture.sessionID.String(), "--socket", fixture.socket})

	// When
	err := command.Execute()
	// Then
	if err != nil {
		t.Fatal(err)
	}
	store, openErr := ai.DefaultSessionStore()
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer store.Close()
	saved, loadErr := store.Load(fixture.sessionID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(saved.Pending) != 1 || saved.Pending[0].CallID != "call_2" {
		t.Fatalf("pending = %+v", saved.Pending)
	}
	socketArg := "--socket '" + fixture.socket + "'"
	for _, command := range []string{
		"qi ai approve " + saved.Pending[0].ApprovalID + " " + socketArg,
		"qi ai resume " + fixture.sessionID.String() + " " + socketArg,
	} {
		if !strings.Contains(output.String(), command) {
			t.Fatalf("output missing %q: %q", command, output.String())
		}
	}
}

func TestAIResumeCLISurfacesDeleteError(t *testing.T) {
	// Given
	fixture := newResumeCLIFixture(t)
	deleteErr := errors.New("forced cleanup failure")
	original := newPlannerSessionStore
	newPlannerSessionStore = func() (plannerSessionStore, error) {
		store, err := ai.DefaultSessionStore()
		if err != nil {
			return nil, err
		}
		return &deleteErrorStore{SessionStore: store, err: deleteErr}, nil
	}
	t.Cleanup(func() { newPlannerSessionStore = original })
	command := newAIResumeCommand()
	command.SetArgs([]string{fixture.sessionID.String(), "--socket", fixture.socket})

	// When
	err := command.Execute()

	// Then
	if err == nil || !strings.Contains(err.Error(), "delete planner session: forced cleanup failure") {
		t.Fatalf("error = %v", err)
	}
	store, openErr := ai.DefaultSessionStore()
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer store.Close()
	if _, loadErr := store.Load(fixture.sessionID); loadErr != nil {
		t.Fatalf("session deleted after cleanup error: %v", loadErr)
	}
}

func TestAIResumeCLIRejectsConcurrentResume(t *testing.T) {
	// Given
	fixture := newResumeCLIFixture(t)
	store, err := ai.DefaultSessionStore()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	lease, err := store.AcquireLease(fixture.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	command := newAIResumeCommand()
	command.SetArgs([]string{fixture.sessionID.String(), "--socket", filepath.Join(t.TempDir(), "absent.sock")})

	// When
	err = command.Execute()

	// Then
	if !errors.Is(err, ai.ErrSessionLeaseHeld) {
		t.Fatalf("error = %v, want lease held", err)
	}
}

type resumeCLIFixture struct {
	sessionID ai.SessionID
	socket    string
}

func newResumeCLIFixture(t *testing.T) resumeCLIFixture {
	t.Helper()
	return newResumeCLIFixtureOutcome(t, "executed")
}

func newResumeCLIFixtureOutcome(t *testing.T, outcome string) resumeCLIFixture {
	t.Helper()
	stateHome := t.TempDir()
	configHome := t.TempDir()
	vault := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("OPENAI_API_KEY", "fresh-secret")

	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if outcome == "second_gate" {
			_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_2","type":"function","function":{"name":"mutate","arguments":"{\"value\":2}"}}]},"finish_reason":"tool_calls"}]}`))
			return
		}
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"resumed"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(provider.Close)
	configDir := filepath.Join(configHome, "qi")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configText := "vault_path = " + quotedTOML(vault) + "\n[[ai.providers]]\n" +
		"provider = \"openai\"\nmodel = \"saved-model\"\nurl = " + quotedTOML(provider.URL) + "\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}

	sessionID, err := ai.GenerateSessionID()
	if err != nil {
		t.Fatal(err)
	}
	auditPath := filepath.Join(t.TempDir(), "audit.log")
	audit, err := approval.OpenAudit(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	queue := approval.NewQueue(audit)
	params := json.RawMessage(`{"value":1}`)
	approvalID, err := queue.EnqueueWithCallID("ai-planner:"+sessionID.String(), "call_1", "mutate", params, "test")
	if err != nil {
		t.Fatal(err)
	}
	switch outcome {
	case "executed", "second_gate":
		if _, err := queue.Approve(approvalID); err != nil {
			t.Fatal(err)
		}
		if _, err := queue.RecordResult(approvalID, json.RawMessage(`{"ok":true}`), nil); err != nil {
			t.Fatal(err)
		}
	case "denied":
		if _, err := queue.Deny(approvalID, "test denial"); err != nil {
			t.Fatal(err)
		}
	case "failed":
		if _, err := queue.Approve(approvalID); err != nil {
			t.Fatal(err)
		}
		if _, err := queue.RecordResult(approvalID, nil, errors.New("test execution failure")); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown outcome %q", outcome)
	}
	if err := audit.Close(); err != nil {
		t.Fatal(err)
	}
	events, err := approval.ReadAuditLog(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	restoredQueue := approval.NewQueue(nil)
	restoredQueue.Restore(events)
	entry, err := buildEntry(
		config.Config{AI: config.AIConfig{Providers: []config.AIProviderConfig{{
			Provider: "openai", Model: "saved-model", URL: provider.URL,
		}}}},
		ai.ProviderOpenAI,
		config.AIProviderConfig{Provider: "openai", Model: "saved-model", URL: provider.URL},
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	session := ai.Session{
		Version:   ai.SessionVersion,
		SessionID: sessionID,
		Provider: ai.ProviderState{Version: ai.ProviderStateVersion, Entries: []ai.ProviderStateEntry{{
			Provider: entry.Provider, Model: entry.Model, ConfigID: entry.ConfigID,
		}}},
		Messages: []ai.Message{
			{Role: ai.RoleUser, Text: "do it"},
			{Role: ai.RoleAssistant, ToolCalls: []ai.ToolCall{{ID: "call_1", Name: "mutate", Input: params}}},
		},
		Pending: []ai.PendingCall{{CallID: "call_1", ApprovalID: approvalID, ToolName: "mutate"}},
	}
	store, err := ai.DefaultSessionStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(session); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	socketRoot, err := os.MkdirTemp("", "qir")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketRoot) })
	socket := filepath.Join(socketRoot, "qid.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	registry := tools.NewRegistry()
	if outcome == "second_gate" {
		if err := registry.RegisterLocal(
			tools.Tool{Name: "mutate", Mutating: true, Schema: json.RawMessage(`{"type":"object"}`)},
			func(context.Context, json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{"ok":true}`), nil
			},
		); err != nil {
			t.Fatal(err)
		}
	}
	server := daemon.NewServer(registry, policy.DefaultDecider{}, restoredQueue, nil)
	go func() { _ = server.Serve(ctx, listener) }()
	t.Cleanup(cancel)
	return resumeCLIFixture{sessionID: sessionID, socket: socket}
}

func quotedTOML(value string) string {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	return strings.TrimSpace(output.String())
}
