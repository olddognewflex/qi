package ai

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// scriptedLLM returns canned responses/errors in order, recording the models
// it was asked for.
type scriptedLLM struct {
	errs   []error // nil entry = success
	calls  int
	models []string
}

func (s *scriptedLLM) Generate(_ context.Context, req GenerateRequest) (*GenerateResponse, error) {
	s.models = append(s.models, req.Model)
	var err error
	if s.calls < len(s.errs) {
		err = s.errs[s.calls]
	}
	s.calls++
	if err != nil {
		return nil, err
	}
	return &GenerateResponse{Text: "ok"}, nil
}

func limitErr(provider string) error {
	return &ProviderError{Provider: provider, StatusCode: 429, Body: "limit"}
}

func TestFallbackSwitchesOnExhaustionAndSticks(t *testing.T) {
	primary := &scriptedLLM{errs: []error{limitErr("ollama")}}
	backup := &scriptedLLM{}
	var switched []string
	fb, err := NewFallbackLLM([]FallbackEntry{
		{Name: "ollama", LLM: primary, Model: "qwen3:14b"},
		{Name: "anthropic", LLM: backup, Model: "claude-x"},
	}, func(from, to FallbackEntry, _ error) {
		switched = append(switched, from.Name+"->"+to.Name)
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := fb.Generate(context.Background(), GenerateRequest{Model: "ignored"})
	if err != nil || resp.Text != "ok" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	if primary.models[0] != "qwen3:14b" {
		t.Errorf("primary got model %q, want entry model", primary.models[0])
	}
	if backup.models[0] != "claude-x" {
		t.Errorf("backup got model %q, want entry model", backup.models[0])
	}
	if len(switched) != 1 || switched[0] != "ollama->anthropic" {
		t.Errorf("switched = %v", switched)
	}

	// Sticky: the next turn goes straight to the backup.
	if _, err := fb.Generate(context.Background(), GenerateRequest{}); err != nil {
		t.Fatal(err)
	}
	if primary.calls != 1 {
		t.Errorf("primary called %d times, want 1", primary.calls)
	}
	if backup.calls != 2 {
		t.Errorf("backup called %d times, want 2", backup.calls)
	}
}

func TestFallbackDoesNotSwitchOnLocalError(t *testing.T) {
	primary := &scriptedLLM{errs: []error{errors.New("ai: unknown role \"weird\"")}}
	backup := &scriptedLLM{}
	fb, _ := NewFallbackLLM([]FallbackEntry{
		{Name: "a", LLM: primary},
		{Name: "b", LLM: backup},
	}, nil)

	if _, err := fb.Generate(context.Background(), GenerateRequest{}); err == nil {
		t.Fatal("want error")
	}
	if backup.calls != 0 {
		t.Error("backup should not be tried on a non-provider error")
	}
}

func TestFallbackDoesNotSwitchOnContextCancel(t *testing.T) {
	primary := &scriptedLLM{errs: []error{context.Canceled}}
	backup := &scriptedLLM{}
	fb, _ := NewFallbackLLM([]FallbackEntry{
		{Name: "a", LLM: primary},
		{Name: "b", LLM: backup},
	}, nil)

	if _, err := fb.Generate(context.Background(), GenerateRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if backup.calls != 0 {
		t.Error("backup should not be tried after cancellation")
	}
}

func TestFallbackAllProvidersFail(t *testing.T) {
	a := &scriptedLLM{errs: []error{limitErr("a")}}
	b := &scriptedLLM{errs: []error{limitErr("b")}}
	fb, _ := NewFallbackLLM([]FallbackEntry{
		{Name: "a", LLM: a},
		{Name: "b", LLM: b},
	}, nil)

	_, err := fb.Generate(context.Background(), GenerateRequest{})
	if err == nil || !strings.Contains(err.Error(), "all providers failed") {
		t.Fatalf("err = %v", err)
	}
	// A later call reports exhaustion without re-probing anything.
	_, err = fb.Generate(context.Background(), GenerateRequest{})
	if err == nil || !strings.Contains(err.Error(), "exhausted") {
		t.Fatalf("err = %v", err)
	}
	if a.calls != 1 || b.calls != 1 {
		t.Errorf("calls = %d/%d, want 1/1", a.calls, b.calls)
	}
}

func TestFallbackSwitchesOnTransportError(t *testing.T) {
	// A dead local Ollama daemon surfaces as a *url.Error; the chain should
	// move on rather than fail the run.
	dead := NewOllamaProvider("http://127.0.0.1:1", "", nil)
	backup := &scriptedLLM{}
	fb, _ := NewFallbackLLM([]FallbackEntry{
		{Name: "ollama", LLM: dead},
		{Name: "backup", LLM: backup},
	}, nil)

	resp, err := fb.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Text: "hi"}}})
	if err != nil || resp.Text != "ok" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
}

func TestFallbackStateRoundTripKeepsActiveEntry(t *testing.T) {
	// Given
	primary := &scriptedLLM{errs: []error{limitErr("ollama")}}
	backup := &scriptedLLM{}
	entries := []FallbackEntry{
		{Name: "ollama", Provider: ProviderOllama, LLM: primary, Model: "qwen3:14b", ConfigID: ConfigIDForDescriptor("ollama|http://localhost:11434")},
		{Name: "anthropic", Provider: ProviderAnthropic, LLM: backup, Model: "claude-x", ConfigID: ConfigIDForDescriptor("anthropic|default")},
	}
	fallback, err := NewFallbackLLM(entries, nil)
	if err != nil {
		t.Fatalf("new fallback: %v", err)
	}
	if _, err := fallback.Generate(context.Background(), GenerateRequest{}); err != nil {
		t.Fatalf("fail over: %v", err)
	}

	// When
	state, err := fallback.ProviderState()
	if err != nil {
		t.Fatalf("snapshot fallback: %v", err)
	}
	restoredPrimary := &scriptedLLM{}
	restoredBackup := &scriptedLLM{}
	restored, err := NewFallbackLLMFromState([]FallbackEntry{
		{Name: "ollama", Provider: ProviderOllama, LLM: restoredPrimary, Model: "qwen3:14b", ConfigID: ConfigIDForDescriptor("ollama|http://localhost:11434")},
		{Name: "anthropic", Provider: ProviderAnthropic, LLM: restoredBackup, Model: "claude-x", ConfigID: ConfigIDForDescriptor("anthropic|default")},
	}, state, nil)
	if err != nil {
		t.Fatalf("restore fallback: %v", err)
	}
	if _, err := restored.Generate(context.Background(), GenerateRequest{}); err != nil {
		t.Fatalf("generate from restored fallback: %v", err)
	}

	// Then
	if restoredPrimary.calls != 0 {
		t.Errorf("restored primary calls = %d, want 0", restoredPrimary.calls)
	}
	if restoredBackup.calls != 1 {
		t.Errorf("restored backup calls = %d, want 1", restoredBackup.calls)
	}
}

func TestFallbackRestoredEntryCanFailOver(t *testing.T) {
	// Given
	primary := &scriptedLLM{}
	backup := &scriptedLLM{errs: []error{limitErr("anthropic")}}
	later := &scriptedLLM{}
	entries := []FallbackEntry{
		{Name: "ollama", Provider: ProviderOllama, LLM: primary, Model: "qwen3:14b", ConfigID: ConfigIDForDescriptor("ollama|http://localhost:11434")},
		{Name: "anthropic", Provider: ProviderAnthropic, LLM: backup, Model: "claude-x", ConfigID: ConfigIDForDescriptor("anthropic|default")},
		{Name: "openai", Provider: ProviderOpenAI, LLM: later, Model: "gpt-x", ConfigID: ConfigIDForDescriptor("openai|https://api.openai.com/v1")},
	}
	state := ProviderState{Version: ProviderStateVersion, Entries: []ProviderStateEntry{
		{Provider: ProviderOllama, Model: "qwen3:14b", ConfigID: ConfigIDForDescriptor("ollama|http://localhost:11434")},
		{Provider: ProviderAnthropic, Model: "claude-x", ConfigID: ConfigIDForDescriptor("anthropic|default")},
		{Provider: ProviderOpenAI, Model: "gpt-x", ConfigID: ConfigIDForDescriptor("openai|https://api.openai.com/v1")},
	}, Active: 1}
	var switches []string
	restored, err := NewFallbackLLMFromState(entries, state, func(from, to FallbackEntry, _ error) {
		switches = append(switches, from.Name+"->"+to.Name)
	})
	if err != nil {
		t.Fatalf("restore fallback: %v", err)
	}

	// When
	if _, err := restored.Generate(context.Background(), GenerateRequest{}); err != nil {
		t.Fatalf("generate from restored fallback: %v", err)
	}

	// Then
	if primary.calls != 0 {
		t.Errorf("primary calls = %d, want 0", primary.calls)
	}
	if backup.models[0] != "claude-x" || later.models[0] != "gpt-x" {
		t.Errorf("models = %q/%q, want claude-x/gpt-x", backup.models[0], later.models[0])
	}
	if got, want := strings.Join(switches, ","), "anthropic->openai"; got != want {
		t.Errorf("switches = %q, want %q", got, want)
	}
}
