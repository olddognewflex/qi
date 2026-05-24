package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestExecuteLocalHandler(t *testing.T) {
	r := NewRegistry()
	echo := func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		return params, nil
	}
	tool := Tool{Name: "echo", Description: "echo params"}
	if err := r.RegisterLocal(tool, echo); err != nil {
		t.Fatalf("register: %v", err)
	}

	in := json.RawMessage(`{"hello":"world"}`)
	out, err := Execute(context.Background(), r, "echo", in)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if string(out) != string(in) {
		t.Fatalf("out = %s, want %s", out, in)
	}
}

func TestExecuteUnknownTool(t *testing.T) {
	r := NewRegistry()
	_, err := Execute(context.Background(), r, "ghost", nil)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestExecuteMCPNotImplemented(t *testing.T) {
	r := NewRegistry()
	src := Source{Kind: SourceMCP, ID: "github"}
	if err := r.RegisterDynamic(src, []Tool{{Name: "create_issue", Source: src}}); err != nil {
		t.Fatalf("register: %v", err)
	}
	_, err := Execute(context.Background(), r, "create_issue", nil)
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("err = %v, want ErrNotImplemented", err)
	}
}

func TestExecutePropagatesHandlerError(t *testing.T) {
	r := NewRegistry()
	boom := errors.New("boom")
	h := func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		return nil, boom
	}
	if err := r.RegisterLocal(Tool{Name: "fail"}, h); err != nil {
		t.Fatalf("register: %v", err)
	}
	_, err := Execute(context.Background(), r, "fail", nil)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestRegisterLocalNilHandlerRejected(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterLocal(Tool{Name: "x"}, nil); err == nil {
		t.Fatal("expected error for nil handler")
	}
}

func TestRegisterLocalOverridesSourceKind(t *testing.T) {
	r := NewRegistry()
	h := func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) { return nil, nil }
	tool := Tool{Name: "x", Source: Source{Kind: SourceMCP, ID: "ignored"}}
	if err := r.RegisterLocal(tool, h); err != nil {
		t.Fatalf("register: %v", err)
	}
	got, _ := r.Lookup("x")
	if got.Source.Kind != SourceLocal || got.Source.ID != "" {
		t.Fatalf("source = %+v, want local with empty id", got.Source)
	}
}

func TestUnregisterLocalDropsHandler(t *testing.T) {
	r := NewRegistry()
	h := func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) { return nil, nil }
	if err := r.RegisterLocal(Tool{Name: "x"}, h); err != nil {
		t.Fatalf("register: %v", err)
	}
	if n := r.Unregister(Source{Kind: SourceLocal}); n != 1 {
		t.Fatalf("removed = %d, want 1", n)
	}
	if _, ok := r.handler("x"); ok {
		t.Fatal("handler should have been dropped")
	}
}
