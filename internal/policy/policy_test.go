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

func TestRemoteDeciderAllowlistedAllows(t *testing.T) {
	d := NewRemoteDecider("task.add", "vault.capture")
	v := d.Decide(context.Background(), Request{
		Caller: CallerRemote,
		Tool:   tools.Tool{Name: "task.add", Mutating: true},
	})
	if v.Decision != Allow {
		t.Fatalf("decision = %v, want Allow", v.Decision)
	}
}

func TestRemoteDeciderNonAllowlistedDenies(t *testing.T) {
	d := NewRemoteDecider("task.add")
	v := d.Decide(context.Background(), Request{
		Caller: CallerRemote,
		Tool:   tools.Tool{Name: "vault.delete", Mutating: true},
	})
	if v.Decision != Deny {
		t.Fatalf("decision = %v, want Deny", v.Decision)
	}
}

func TestRemoteDeciderFallsThroughForOtherCallers(t *testing.T) {
	d := NewRemoteDecider("task.add")
	// A non-remote caller is governed by the base decider, not the allowlist:
	// cli still allowed, ai-planner mutation still confirmed.
	if v := d.Decide(context.Background(), Request{
		Caller: CallerCLI,
		Tool:   tools.Tool{Name: "vault.delete", Mutating: true},
	}); v.Decision != Allow {
		t.Fatalf("cli decision = %v, want Allow", v.Decision)
	}
	if v := d.Decide(context.Background(), Request{
		Caller: "ai-planner",
		Tool:   tools.Tool{Name: "task.add", Mutating: true},
	}); v.Decision != Confirm {
		t.Fatalf("ai-planner decision = %v, want Confirm", v.Decision)
	}
}
