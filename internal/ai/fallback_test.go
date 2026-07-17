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
