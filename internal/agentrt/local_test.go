package agentrt

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestLocalRuntimeListsNothing(t *testing.T) {
	l := NewLocalRuntime()
	if l.Name() != "local" {
		t.Fatalf("Name() = %q", l.Name())
	}
	ws, err := l.ListWorkspaces(context.Background())
	if err != nil || ws == nil || len(ws) != 0 {
		t.Fatalf("ListWorkspaces = %v, %v; want empty non-nil slice", ws, err)
	}
	ag, err := l.ListAgents(context.Background())
	if err != nil || ag == nil || len(ag) != 0 {
		t.Fatalf("ListAgents = %v, %v; want empty non-nil slice", ag, err)
	}
	f, err := l.Focused(context.Background())
	if err != nil || f != (Focus{}) {
		t.Fatalf("Focused = %+v, %v", f, err)
	}
}

func TestLocalRuntimeAddressingIsUnavailable(t *testing.T) {
	l := NewLocalRuntime()
	ctx := context.Background()

	_, err := l.GetAgent(ctx, "w9:p1")
	checkUnavailable(t, "GetAgent", err)

	checkUnavailable(t, "Send", l.Send(ctx, "w9:p1", "hello"))

	_, err = l.ReadOutput(ctx, "w9:p1", OutputOptions{})
	checkUnavailable(t, "ReadOutput", err)

	_, err = l.WaitForState(ctx, "w9:p1")
	checkUnavailable(t, "WaitForState", err)
}

func checkUnavailable(t *testing.T, op string, err error) {
	t.Helper()
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("%s err = %v, want ErrUnavailable", op, err)
	}
	if errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("%s err = %v, must not claim the agent is missing", op, err)
	}
	// The message has to tell the user what to fix.
	if !strings.Contains(err.Error(), "Herdr") {
		t.Fatalf("%s err = %v, want it to name Herdr", op, err)
	}
}
