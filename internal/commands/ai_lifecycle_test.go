package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"qi/internal/ai"
	"qi/internal/approval"
	"qi/internal/daemon"
	"qi/internal/policy"
	"qi/internal/tools"
)

func TestAIPlannerCLIRunDecisionRestartResume(t *testing.T) {
	for _, decision := range []string{"approve", "deny"} {
		t.Run(decision, func(t *testing.T) {
			stateHome := t.TempDir()
			configHome := t.TempDir()
			vault := t.TempDir()
			t.Setenv("XDG_STATE_HOME", stateHome)
			t.Setenv("XDG_CONFIG_HOME", configHome)
			t.Setenv("OPENAI_API_KEY", "fresh-secret")

			var providerCalls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				if providerCalls.Add(1) == 1 {
					_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"mutate","arguments":"{\"value\":1}"}}]},"finish_reason":"tool_calls"}]}`))
					return
				}
				_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"continued"},"finish_reason":"stop"}]}`))
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

			socketRoot, err := os.MkdirTemp("", "qi63")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(socketRoot) })
			socket := filepath.Join(socketRoot, "qid.sock")
			auditPath := filepath.Join(socketRoot, "audit.log")
			audit, err := approval.OpenAudit(auditPath)
			if err != nil {
				t.Fatal(err)
			}

			var mutations atomic.Int32
			registry := tools.NewRegistry()
			if err := registry.RegisterLocal(
				tools.Tool{Name: "mutate", Mutating: true, Schema: json.RawMessage(`{"type":"object"}`)},
				func(context.Context, json.RawMessage) (json.RawMessage, error) {
					mutations.Add(1)
					return json.RawMessage(`{"ok":true}`), nil
				},
			); err != nil {
				t.Fatal(err)
			}
			queue := approval.NewQueue(audit)
			stop := startPlannerTestDaemon(t, socket, registry, queue)
			t.Cleanup(stop)

			var runOutput bytes.Buffer
			run := newAIRunCommand()
			run.SetOut(&runOutput)
			run.SetArgs([]string{"do it", "--socket", socket})
			if err := run.Execute(); err != nil {
				t.Fatal(err)
			}
			approvalID := firstMatch(t, runOutput.String(), `qi ai approve ([0-9a-f]+)`)
			sessionText := firstMatch(t, runOutput.String(), `qi ai resume ([0-9a-f]{64})`)
			sessionID, err := ai.ParseSessionID(sessionText)
			if err != nil {
				t.Fatal(err)
			}
			socketArg := "--socket '" + socket + "'"
			if !strings.Contains(runOutput.String(), "qi ai approve "+approvalID+" "+socketArg) ||
				!strings.Contains(runOutput.String(), "qi ai resume "+sessionText+" "+socketArg) {
				t.Fatalf("run instructions lost custom socket: %q", runOutput.String())
			}

			switch decision {
			case "approve":
				approve := newAIApproveCommand()
				approve.SetArgs([]string{approvalID, "--socket", socket})
				if err := approve.Execute(); err != nil {
					t.Fatal(err)
				}
			case "deny":
				deny := newAIDenyCommand()
				deny.SetArgs([]string{approvalID, "--reason", "test denial", "--socket", socket})
				if err := deny.Execute(); err != nil {
					t.Fatal(err)
				}
			}

			stop()
			if err := audit.Close(); err != nil {
				t.Fatal(err)
			}
			entries, err := approval.ReadAuditLog(auditPath)
			if err != nil {
				t.Fatal(err)
			}
			audit, err = approval.OpenAudit(auditPath)
			if err != nil {
				t.Fatal(err)
			}
			freshQueue := approval.NewQueue(audit)
			freshQueue.Restore(entries)
			stop = startPlannerTestDaemon(t, socket, registry, freshQueue)
			t.Cleanup(stop)

			var resumeOutput bytes.Buffer
			resume := newAIResumeCommand()
			resume.SetOut(&resumeOutput)
			resume.SetArgs([]string{sessionText, "--socket", socket})
			if err := resume.Execute(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(resumeOutput.String(), "continued") {
				t.Fatalf("resume output = %q", resumeOutput.String())
			}
			wantMutations := int32(0)
			if decision == "approve" {
				wantMutations = 1
			}
			if mutations.Load() != wantMutations {
				t.Fatalf("mutations = %d, want %d", mutations.Load(), wantMutations)
			}
			if providerCalls.Load() != 2 {
				t.Fatalf("provider calls = %d, want 2", providerCalls.Load())
			}
			store, err := ai.DefaultSessionStore()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Load(sessionID); err == nil {
				t.Fatal("completed session was not deleted")
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			stop()
			if err := audit.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func startPlannerTestDaemon(t *testing.T, socket string, registry *tools.Registry, queue *approval.Queue) func() {
	t.Helper()
	listener, err := daemon.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	server := daemon.NewServer(registry, policy.DefaultDecider{}, queue, nil)
	go func() {
		_ = server.Serve(ctx, listener)
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !daemon.Alive(socket) {
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("daemon did not start on %s", socket)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("daemon did not stop on %s", socket)
		}
	}
}

func firstMatch(t *testing.T, text, pattern string) string {
	t.Helper()
	match := regexp.MustCompile(pattern).FindStringSubmatch(text)
	if len(match) != 2 {
		t.Fatalf("output %q did not match %q", text, pattern)
	}
	return match[1]
}

func TestPlannerLifecycleInvalidSessionIDFailsBeforeDial(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	resume := newAIResumeCommand()
	resume.SetArgs([]string{"../../outside", "--socket", filepath.Join(t.TempDir(), "absent.sock")})
	err := resume.Execute()
	if !errors.Is(err, ai.ErrInvalidSessionID) {
		t.Fatalf("error = %v, want invalid session id", err)
	}
}

func TestPlannerInstructionsShellQuoteCustomSocket(t *testing.T) {
	result := ai.RunResult{
		StopReason: ai.StopAwaitingApproval,
		SessionID:  strings.Repeat("a", 64),
		Pending:    []ai.PendingCall{{ApprovalID: "approval", ToolName: "mutate"}},
	}
	var output bytes.Buffer
	printPlannerResult(&output, result, "/tmp/a'$(unsafe)/qid.sock")
	want := `--socket '/tmp/a'"'"'$(unsafe)/qid.sock'`
	if strings.Count(output.String(), want) != 2 {
		t.Fatalf("socket argument was not safely quoted in both commands: %q", output.String())
	}
}
