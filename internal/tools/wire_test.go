package tools

import (
	"encoding/json"
	"testing"
)

func TestSourceKindJSONRoundtrip(t *testing.T) {
	cases := []struct {
		kind SourceKind
		json string
	}{
		{SourceLocal, `"local"`},
		{SourceMCP, `"mcp"`},
		{SourceSkill, `"skill"`},
	}
	for _, c := range cases {
		b, err := json.Marshal(c.kind)
		if err != nil {
			t.Fatalf("marshal %v: %v", c.kind, err)
		}
		if string(b) != c.json {
			t.Fatalf("marshal %v = %s, want %s", c.kind, b, c.json)
		}
		var got SourceKind
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("unmarshal %s: %v", b, err)
		}
		if got != c.kind {
			t.Fatalf("roundtrip = %v, want %v", got, c.kind)
		}
	}
}

func TestSourceKindRejectsInvalid(t *testing.T) {
	if _, err := json.Marshal(SourceKind(99)); err == nil {
		t.Fatal("expected marshal error for invalid kind")
	}
	var k SourceKind
	if err := json.Unmarshal([]byte(`"bogus"`), &k); err == nil {
		t.Fatal("expected unmarshal error for bogus kind")
	}
}

func TestToolJSONShape(t *testing.T) {
	tool := Tool{
		Name:        "vault.capture",
		Source:      Source{Kind: SourceLocal},
		Version:     "1",
		Mutating:    true,
		Description: "Capture text",
		Schema:      json.RawMessage(`{"type":"object"}`),
	}
	b, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(b, &generic); err != nil {
		t.Fatalf("unmarshal generic: %v", err)
	}
	if generic["name"] != "vault.capture" {
		t.Fatalf("name = %v", generic["name"])
	}
	src, _ := generic["source"].(map[string]any)
	if src["kind"] != "local" {
		t.Fatalf("source.kind = %v, want \"local\"", src["kind"])
	}
	if _, hasID := src["id"]; hasID {
		t.Fatalf("source.id should be omitted when empty, got %v", src["id"])
	}
}
