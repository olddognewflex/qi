package ai

import (
	"context"
	"strings"
	"testing"
)

func TestValidLevel(t *testing.T) {
	for _, l := range []string{"", "coarse", "normal", "fine", "FINE", " normal "} {
		if !ValidLevel(l) {
			t.Errorf("ValidLevel(%q) = false, want true", l)
		}
	}
	for _, l := range []string{"medium", "deep", "1", "x"} {
		if ValidLevel(l) {
			t.Errorf("ValidLevel(%q) = true, want false", l)
		}
	}
}

func TestNormalizeLevel(t *testing.T) {
	if got := NormalizeLevel(""); got != DefaultBreakdownLevel {
		t.Errorf("NormalizeLevel(\"\") = %q, want %q", got, DefaultBreakdownLevel)
	}
	if got := NormalizeLevel("  FINE "); got != "fine" {
		t.Errorf("NormalizeLevel = %q, want fine", got)
	}
}

func TestBreakdown_ParsesJSONArray(t *testing.T) {
	llm := &stubLLM{responses: []*GenerateResponse{
		textResp(`["Cut changelog", "Tag v2", "Publish release notes"]`),
	}}
	got, err := Breakdown(context.Background(), llm, "", "Ship release", "normal")
	if err != nil {
		t.Fatalf("Breakdown: %v", err)
	}
	want := []string{"Cut changelog", "Tag v2", "Publish release notes"}
	if len(got) != len(want) {
		t.Fatalf("got %d subtasks, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("subtask[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestBreakdown_StripsProseAndFences(t *testing.T) {
	// Models sometimes wrap the array despite instructions; the outermost [...] is isolated.
	llm := &stubLLM{responses: []*GenerateResponse{
		textResp("Sure! Here you go:\n```json\n[\"A\", \"\", \"B\"]\n```\nHope that helps."),
	}}
	got, err := Breakdown(context.Background(), llm, "", "Do work", "coarse")
	if err != nil {
		t.Fatalf("Breakdown: %v", err)
	}
	// Blank entries dropped.
	if len(got) != 2 || got[0] != "A" || got[1] != "B" {
		t.Fatalf("got %v, want [A B]", got)
	}
}

func TestBreakdown_EmptyTextRejected(t *testing.T) {
	llm := &stubLLM{responses: []*GenerateResponse{textResp("[]")}}
	if _, err := Breakdown(context.Background(), llm, "", "   ", "normal"); err == nil {
		t.Fatal("expected error for empty task text")
	}
}

func TestBreakdown_NoArrayInResponse(t *testing.T) {
	llm := &stubLLM{responses: []*GenerateResponse{textResp("I cannot help with that.")}}
	_, err := Breakdown(context.Background(), llm, "", "Ship release", "normal")
	if err == nil || !strings.Contains(err.Error(), "no JSON array") {
		t.Fatalf("expected no-array error, got %v", err)
	}
}

func TestBreakdown_EmptyArrayRejected(t *testing.T) {
	llm := &stubLLM{responses: []*GenerateResponse{textResp("[]")}}
	_, err := Breakdown(context.Background(), llm, "", "Ship release", "normal")
	if err == nil || !strings.Contains(err.Error(), "no subtasks") {
		t.Fatalf("expected no-subtasks error, got %v", err)
	}
}
