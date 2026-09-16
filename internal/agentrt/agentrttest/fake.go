// Package agentrttest provides an in-memory agentrt.Runtime for tests of
// everything above the runtime boundary (resolution, voice, commands).
package agentrttest

import (
	"context"
	"sync"

	"qi/internal/agentrt"
)

// Sent records one Send call.
type Sent struct {
	ID      agentrt.AgentID
	Message string
}

// Fake is a scriptable agentrt.Runtime. Zero value is usable: no
// workspaces, no agents, nothing focused.
type Fake struct {
	mu sync.Mutex

	Workspaces []agentrt.Workspace
	Agents     []agentrt.AgentInstance
	Focus      agentrt.Focus

	// Outputs maps agent ID -> text returned by ReadOutput.
	Outputs map[agentrt.AgentID]string
	// WaitStates maps agent ID -> state returned by WaitForState. Missing
	// entries return the agent's current State.
	WaitStates map[agentrt.AgentID]agentrt.State

	// Err, when set, is returned by every method (simulates an unavailable
	// runtime). Per-method overrides below take precedence when set.
	Err     error
	SendErr error
	WaitErr error

	// SentMessages accumulates every accepted Send call.
	SentMessages []Sent
	// Waits accumulates every WaitForState call's requested states.
	Waits [][]agentrt.State
}

var _ agentrt.Runtime = (*Fake)(nil)

func (f *Fake) Name() string { return "fake" }

func (f *Fake) ListWorkspaces(ctx context.Context) ([]agentrt.Workspace, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agentrt.Workspace(nil), f.Workspaces...), nil
}

func (f *Fake) ListAgents(ctx context.Context) ([]agentrt.AgentInstance, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agentrt.AgentInstance(nil), f.Agents...), nil
}

func (f *Fake) GetAgent(ctx context.Context, id agentrt.AgentID) (agentrt.AgentInstance, error) {
	if f.Err != nil {
		return agentrt.AgentInstance{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.Agents {
		if a.ID == id {
			return a, nil
		}
	}
	return agentrt.AgentInstance{}, &agentrt.RuntimeError{Code: "agent_not_found", Message: string(id), Err: agentrt.ErrAgentNotFound}
}

func (f *Fake) Focused(ctx context.Context) (agentrt.Focus, error) {
	if f.Err != nil {
		return agentrt.Focus{}, f.Err
	}
	return f.Focus, nil
}

func (f *Fake) Send(ctx context.Context, id agentrt.AgentID, message string) error {
	if f.Err != nil {
		return f.Err
	}
	if f.SendErr != nil {
		return f.SendErr
	}
	a, err := f.GetAgent(ctx, id)
	if err != nil {
		return err
	}
	if a.State == agentrt.StateBlocked {
		return &agentrt.RuntimeError{Code: "agent_blocked", Message: string(id), Err: agentrt.ErrAgentBlocked}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SentMessages = append(f.SentMessages, Sent{ID: id, Message: message})
	return nil
}

func (f *Fake) ReadOutput(ctx context.Context, id agentrt.AgentID, opts agentrt.OutputOptions) (agentrt.AgentOutput, error) {
	if f.Err != nil {
		return agentrt.AgentOutput{}, f.Err
	}
	if _, err := f.GetAgent(ctx, id); err != nil {
		return agentrt.AgentOutput{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return agentrt.AgentOutput{Text: f.Outputs[id]}, nil
}

func (f *Fake) WaitForState(ctx context.Context, id agentrt.AgentID, states ...agentrt.State) (agentrt.State, error) {
	if f.Err != nil {
		return "", f.Err
	}
	if f.WaitErr != nil {
		return "", f.WaitErr
	}
	a, err := f.GetAgent(ctx, id)
	if err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Waits = append(f.Waits, states)
	if s, ok := f.WaitStates[id]; ok {
		return s, nil
	}
	return a.State, nil
}

// SetState mutates an agent's state in place (simulates a lifecycle change).
func (f *Fake) SetState(id agentrt.AgentID, s agentrt.State) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.Agents {
		if f.Agents[i].ID == id {
			f.Agents[i].State = s
		}
	}
}
