package policy

import (
	"context"
	"testing"

	"qi/internal/tools"
)

func TestDefaultDeciderMissingCaller(t *testing.T) {
	v := DefaultDecider{}.Decide(context.Background(), Request{Tool: tools.Tool{Name: "x"}})
	if v.Decision != Deny {
		t.Fatalf("decision = %v, want Deny", v.Decision)
	}
}

func TestDefaultDeciderCLIAllows(t *testing.T) {
	v := DefaultDecider{}.Decide(context.Background(), Request{
		Caller: CallerCLI,
		Tool:   tools.Tool{Name: "vault.capture", Mutating: true},
	})
	if v.Decision != Allow {
		t.Fatalf("decision = %v, want Allow", v.Decision)
	}
}

func TestDefaultDeciderReadOnlyAllows(t *testing.T) {
	v := DefaultDecider{}.Decide(context.Background(), Request{
		Caller: "ai-planner",
		Tool:   tools.Tool{Name: "ro", Mutating: false},
	})
	if v.Decision != Allow {
		t.Fatalf("decision = %v, want Allow", v.Decision)
	}
}

func TestDefaultDeciderMutatingNonCLIConfirms(t *testing.T) {
	v := DefaultDecider{}.Decide(context.Background(), Request{
		Caller: "ai-planner",
		Tool:   tools.Tool{Name: "vault.capture", Mutating: true},
	})
	if v.Decision != Confirm {
		t.Fatalf("decision = %v, want Confirm", v.Decision)
	}
	if v.Reason == "" {
		t.Fatal("expected non-empty reason")
	}
}
