package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qi/internal/tools"
)

func TestRegisterCaptureRoundtrip(t *testing.T) {
	inbox := t.TempDir()
	r := tools.NewRegistry()
	if err := RegisterCapture(r, inbox); err != nil {
		t.Fatalf("register: %v", err)
	}

	got, ok := r.Lookup(CaptureToolName)
	if !ok {
		t.Fatalf("lookup miss for %s", CaptureToolName)
	}
	if !got.Mutating {
		t.Fatal("capture tool should be marked mutating")
	}

	params, err := json.Marshal(captureParams{Text: "Buy oat milk"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := tools.Execute(context.Background(), r, CaptureToolName, params)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var res captureResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if filepath.Dir(res.Path) != inbox {
		t.Fatalf("path %q not in inbox %q", res.Path, inbox)
	}
	data, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if !strings.Contains(string(data), "Buy oat milk") {
		t.Fatalf("written file missing capture text: %q", data)
	}
}

func TestRegisterCaptureEmptyInbox(t *testing.T) {
	r := tools.NewRegistry()
	if err := RegisterCapture(r, ""); err == nil {
		t.Fatal("expected error for empty inbox dir")
	}
}

func TestCaptureRejectsEmptyText(t *testing.T) {
	r := tools.NewRegistry()
	if err := RegisterCapture(r, t.TempDir()); err != nil {
		t.Fatalf("register: %v", err)
	}
	params, _ := json.Marshal(captureParams{Text: "   "})
	_, err := tools.Execute(context.Background(), r, CaptureToolName, params)
	if err == nil {
		t.Fatal("expected empty-text error")
	}
}

func TestCaptureRejectsMalformedJSON(t *testing.T) {
	r := tools.NewRegistry()
	if err := RegisterCapture(r, t.TempDir()); err != nil {
		t.Fatalf("register: %v", err)
	}
	_, err := tools.Execute(context.Background(), r, CaptureToolName, json.RawMessage(`{not-json`))
	if err == nil {
		t.Fatal("expected json parse error")
	}
}
