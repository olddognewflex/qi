package agentrt

import (
	"context"
	"fmt"
)

// LocalRuntime is the fallback Runtime for when no runtime authority
// exists: qi is running in a plain terminal with no Herdr server to ask.
//
// It deliberately knows no agents. qi must not build a competing agent
// registry by scanning processes: process discovery cannot answer the
// questions that matter (which pane, which session, what state) and a
// half-right answer is worse than an honest "not detected", which tells
// the user what to fix.
type LocalRuntime struct{}

var _ Runtime = (*LocalRuntime)(nil)

// NewLocalRuntime returns the no-agent fallback runtime.
func NewLocalRuntime() *LocalRuntime { return &LocalRuntime{} }

func (l *LocalRuntime) Name() string { return "local" }

// ListWorkspaces returns no workspaces. Listing nothing is not an error:
// callers render an empty list, they do not report a failure.
func (l *LocalRuntime) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	return []Workspace{}, nil
}

// ListAgents returns no agents, for the same reason as ListWorkspaces.
func (l *LocalRuntime) ListAgents(ctx context.Context) ([]AgentInstance, error) {
	return []AgentInstance{}, nil
}

// unavailable is the single error every addressing operation returns. All
// four use ErrUnavailable rather than ErrAgentNotFound: the agent may well
// exist, qi simply has no runtime that can see it, and the fix is to start
// Herdr rather than to name a different agent.
func unavailable(op string) error {
	return fmt.Errorf("%s: %w: no agent runtime detected (Herdr is not running, or qi cannot reach it)", op, ErrUnavailable)
}

func (l *LocalRuntime) GetAgent(ctx context.Context, id AgentID) (AgentInstance, error) {
	return AgentInstance{}, unavailable("get agent " + string(id))
}

// Focused reports nothing focused, without error: there is no runtime to
// have a focus, which is a fact rather than a failure.
func (l *LocalRuntime) Focused(ctx context.Context) (Focus, error) { return Focus{}, nil }

func (l *LocalRuntime) Send(ctx context.Context, id AgentID, message string) error {
	return unavailable("send to agent " + string(id))
}

func (l *LocalRuntime) ReadOutput(ctx context.Context, id AgentID, opts OutputOptions) (AgentOutput, error) {
	return AgentOutput{}, unavailable("read agent " + string(id))
}

func (l *LocalRuntime) WaitForState(ctx context.Context, id AgentID, states ...State) (State, error) {
	return "", unavailable("wait for agent " + string(id))
}
