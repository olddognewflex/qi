package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"qi/internal/daemon"
)

// findCommand returns the direct subcommand of parent whose Use begins with
// name, or nil.
func findCommand(parent *cobra.Command, name string) *cobra.Command {
	for _, c := range parent.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

// writeFakeQid writes an executable stand-in for the qid binary with a known
// mtime, so staleness can be exercised without building or running anything.
func writeFakeQid(t *testing.T, modTime time.Time) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "qid")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write fake qid: %v", err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	return path
}

func TestDaemonStaleness(t *testing.T) {
	started := time.Now().Add(-time.Hour)

	t.Run("reported exe rebuilt after start is stale", func(t *testing.T) {
		bin := writeFakeQid(t, started.Add(time.Minute))
		got, summary := daemonStaleness(bin, bin, started)
		if got != daemon.StalenessStale {
			t.Fatalf("got %v, want stale", got)
		}
		if !strings.Contains(summary, "qi daemon restart") {
			t.Fatalf("summary %q should name the remedy", summary)
		}
	})

	t.Run("reported exe older than start is current", func(t *testing.T) {
		bin := writeFakeQid(t, started.Add(-time.Minute))
		got, summary := daemonStaleness(bin, bin, started)
		if got != daemon.StalenessCurrent {
			t.Fatalf("got %v, want current", got)
		}
		if !strings.Contains(summary, bin) {
			t.Fatalf("summary %q should name the binary", summary)
		}
	})

	t.Run("unknown start time is unknown", func(t *testing.T) {
		bin := writeFakeQid(t, started)
		got, summary := daemonStaleness(bin, bin, time.Time{})
		if got != daemon.StalenessUnknown {
			t.Fatalf("got %v, want unknown", got)
		}
		if !strings.Contains(summary, "unknown") {
			t.Fatalf("summary = %q", summary)
		}
	})

	t.Run("unstattable exe is unknown", func(t *testing.T) {
		got, summary := daemonStaleness(filepath.Join(t.TempDir(), "gone"), "", started)
		if got != daemon.StalenessUnknown {
			t.Fatalf("got %v, want unknown", got)
		}
		if !strings.Contains(summary, "unknown") {
			t.Fatalf("summary = %q", summary)
		}
	})

	// With no reported exe the fallback resolves the binary qi would start —
	// here pinned by the --qid-bin equivalent so the test never touches $PATH.
	t.Run("falls back to the resolved binary", func(t *testing.T) {
		bin := writeFakeQid(t, started.Add(time.Minute))
		got, _ := daemonStaleness("", bin, started)
		if got != daemon.StalenessStale {
			t.Fatalf("got %v, want stale", got)
		}
	})

	// The case ResolveQidBinary exists for: the daemon is running an older
	// install path while the fresh build sits where qi would find it. That path's
	// mtime is untouched, so only the path comparison can catch it.
	t.Run("a different binary than restart would run is stale", func(t *testing.T) {
		running := writeFakeQid(t, started.Add(-time.Hour))
		wouldStart := writeFakeQid(t, started.Add(-time.Hour))
		got, summary := daemonStaleness(running, wouldStart, started)
		if got != daemon.StalenessStale {
			t.Fatalf("got %v, want stale", got)
		}
		if !strings.Contains(summary, running) || !strings.Contains(summary, wouldStart) {
			t.Fatalf("summary %q should name both binaries", summary)
		}
	})

	t.Run("the same binary reached via a symlink is not a mismatch", func(t *testing.T) {
		real := writeFakeQid(t, started.Add(-time.Hour))
		link := filepath.Join(t.TempDir(), "qid")
		if err := os.Symlink(real, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		got, summary := daemonStaleness(link, real, started)
		if got != daemon.StalenessCurrent {
			t.Fatalf("got %v (%s), want current", got, summary)
		}
	})
}

func TestDaemonStatusReportsNotRunning(t *testing.T) {
	env := &daemonEnv{socket: filepath.Join(t.TempDir(), "absent.sock")}
	var out bytes.Buffer
	if err := env.status(context.Background(), &out); err != nil {
		t.Fatalf("status must not fail when the daemon is down: %v", err)
	}
	if !strings.Contains(out.String(), "not running") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestDaemonStopIsNoOpWhenNotRunning(t *testing.T) {
	env := &daemonEnv{socket: filepath.Join(t.TempDir(), "absent.sock")}
	var out bytes.Buffer
	if err := env.stop(context.Background(), &out); err != nil {
		t.Fatalf("stop must be a no-op when the daemon is down: %v", err)
	}
	if !strings.Contains(out.String(), "not running") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestDaemonStartReportsUnresolvableBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv(daemon.QidBinEnv, "")
	env := &daemonEnv{socket: filepath.Join(t.TempDir(), "absent.sock")}
	err := env.start(context.Background(), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "qid binary not found") {
		t.Fatalf("err = %v, want a clear not-found message", err)
	}
}

// TestDaemonCommandSkipsConfigCheck guards the reason `qi daemon` is wrapped in
// markSkipConfig: lifecycle control must work on a box with no vault configured.
func TestDaemonCommandSkipsConfigCheck(t *testing.T) {
	root := NewRootCommand()
	daemonCmd := findCommand(root, "daemon")
	if daemonCmd == nil {
		t.Fatal("qi daemon is not registered")
	}
	if !skipsConfigCheck(daemonCmd) {
		t.Fatal("qi daemon must be exempt from the valid-config requirement")
	}
	for _, sub := range []string{"status", "start", "stop", "restart"} {
		if findCommand(daemonCmd, sub) == nil {
			t.Errorf("qi daemon %s is not registered", sub)
		}
	}
}
