package commands

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"qi/internal/config"
)

// statsWorker serves GET /stats with the DRAIN token, like the real Worker.
func statsWorker(t *testing.T, pending, deadletter int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/stats", func(rw http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+drainToken {
			rw.WriteHeader(http.StatusUnauthorized)
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(rw, `{"pending":%d,"deadletter":%d}`, pending, deadletter)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func runStatus(t *testing.T, cfg config.Config) string {
	t.Helper()
	cmd := newRemoteStatusCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	return out.String()
}

func TestRemoteStatus_DisabledIsNoOp(t *testing.T) {
	out := runStatus(t, config.Config{})
	if !strings.Contains(out, "remote queue disabled") {
		t.Fatalf("want disabled message, got %q", out)
	}
}

func TestRemoteStatus_ReportsCounts(t *testing.T) {
	ts := statsWorker(t, 3, 1)
	cfg := config.Config{RemoteQueue: config.RemoteQueueConfig{
		Enabled: true,
		URL:     ts.URL,
		Token:   drainToken,
	}}
	out := runStatus(t, cfg)
	if !strings.Contains(out, "pending 3, deadletter 1") {
		t.Fatalf("want counts line, got %q", out)
	}
	if !strings.Contains(out, "--show-failed") {
		t.Fatalf("want deadletter hint, got %q", out)
	}
}

func TestRemoteStatus_NoDeadletterHint(t *testing.T) {
	ts := statsWorker(t, 2, 0)
	cfg := config.Config{RemoteQueue: config.RemoteQueueConfig{
		Enabled: true,
		URL:     ts.URL,
		Token:   drainToken,
	}}
	out := runStatus(t, cfg)
	if !strings.Contains(out, "pending 2, deadletter 0") {
		t.Fatalf("want counts line, got %q", out)
	}
	if strings.Contains(out, "--show-failed") {
		t.Fatalf("should not hint show-failed when no deadletters, got %q", out)
	}
}
