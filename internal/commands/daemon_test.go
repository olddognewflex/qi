package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"qi/internal/daemon"
	"qi/internal/daemon/client"
	"qi/internal/tools"
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

// shortSock returns a socket path under a short temp dir — macOS caps sun_path
// at ~104 bytes and t.TempDir() alone can exceed it.
func shortSock(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "qid69")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "q.sock")
}

// startStoppable brings up an in-process qid stand-in on sock that answers
// `status` (pid 0, so waitProcessGone short-circuits) and `daemon.shutdown`
// (cancels its own serve, closing the listener). Enough for stop's round-trip.
func startStoppable(t *testing.T, sock string) {
	t.Helper()
	ln, err := daemon.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := daemon.NewServer(tools.NewRegistry(), nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	srv.Register("status", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.Marshal(client.DaemonStatus{Pid: 0})
	})
	srv.Register("daemon.shutdown", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		cancel()
		return json.RawMessage(`{"status":"stopping"}`), nil
	})
	t.Cleanup(cancel)
	go func() { _ = srv.Serve(ctx, ln) }()
	waitAlive(t, sock)
}

func waitAlive(t *testing.T, sock string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if daemon.Alive(sock) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket %s never came alive", sock)
}

// superviseRelaunch mimics a KeepAlive supervisor: once sock goes quiet it waits
// a beat (so stop's own WaitGone observes the quiet first, uninterfered), then
// binds a bare accepting listener so waitRespawn sees qid "back". Returns a stop
// func to tear the relaunched listener down.
func superviseRelaunch(t *testing.T, sock string) func() {
	t.Helper()
	lnCh := make(chan net.Listener, 1)
	go func() {
		if !daemon.WaitGone(sock, 3*time.Second) {
			lnCh <- nil
			return
		}
		time.Sleep(250 * time.Millisecond)
		ln, err := daemon.Listen(sock)
		if err != nil {
			lnCh <- nil
			return
		}
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				_ = c.Close()
			}
		}()
		lnCh <- ln
	}()
	return func() {
		if ln := <-lnCh; ln != nil {
			_ = ln.Close()
		}
	}
}

// stubSupervisor replaces the launchd probe for the duration of a test, so the
// real launchctl call (which would see the developer's own loaded qid agent)
// never contaminates a stop.
func stubSupervisor(t *testing.T, label string, supervised, known bool) {
	t.Helper()
	old := supervisorProbe
	supervisorProbe = func(context.Context) (string, bool, bool) { return label, supervised, known }
	t.Cleanup(func() { supervisorProbe = old })
}

func TestWaitRespawn(t *testing.T) {
	t.Run("returns true when the socket comes back alive", func(t *testing.T) {
		sock := shortSock(t)
		startStoppable(t, sock) // a live listener is up
		if !waitRespawn(sock, time.Second) {
			t.Fatal("waitRespawn should detect the live socket")
		}
	})
	t.Run("returns false when the socket stays quiet", func(t *testing.T) {
		sock := shortSock(t)
		start := time.Now()
		if waitRespawn(sock, 300*time.Millisecond) {
			t.Fatal("waitRespawn should not fire on an absent socket")
		}
		if time.Since(start) < 250*time.Millisecond {
			t.Fatal("waitRespawn returned early; it should watch the whole window")
		}
	})
}

// TestDaemonStopDetectsLaunchdAgent covers the primary, timing-independent
// signal: a loaded launchd agent. The probe reports supervised, so stop warns
// immediately (no respawn poll needed) and splices the label into the remedy.
func TestDaemonStopDetectsLaunchdAgent(t *testing.T) {
	sock := shortSock(t)
	startStoppable(t, sock)
	stubSupervisor(t, "com.olddognewflex.qid", true, true)

	env := &daemonEnv{socket: sock}
	var out bytes.Buffer
	respawned, err := env.stop(context.Background(), &out)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !respawned {
		t.Fatalf("a loaded launchd agent must be reported; out=%q", out.String())
	}
	if strings.Contains(out.String(), "qid stopped") {
		t.Errorf("must not claim a clean stop under a supervisor: %q", out.String())
	}
	for _, want := range []string{"com.olddognewflex.qid", "launchctl", "bootout"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("warning missing %q: %q", want, out.String())
		}
	}
}

// TestDaemonStopWarnsOnGenericRespawn covers the fallback path: when the probe
// can't introspect the supervisor (non-macOS, or launchctl unavailable — known
// is false), stop watches for the respawn instead and warns generically.
func TestDaemonStopWarnsOnGenericRespawn(t *testing.T) {
	sock := shortSock(t)
	startStoppable(t, sock)
	stubSupervisor(t, "", false, false) // undetermined → respawn-poll fallback

	oldWindow := supervisorRecheckWindow
	supervisorRecheckWindow = 2 * time.Second
	t.Cleanup(func() { supervisorRecheckWindow = oldWindow })

	teardown := superviseRelaunch(t, sock)
	t.Cleanup(teardown)

	env := &daemonEnv{socket: sock}
	var out bytes.Buffer
	respawned, err := env.stop(context.Background(), &out)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !respawned {
		t.Fatalf("expected respawn to be detected; out=%q", out.String())
	}
	if strings.Contains(out.String(), "qid stopped") {
		t.Errorf("must not claim a clean stop when relaunched: %q", out.String())
	}
	if !strings.Contains(out.String(), "supervisor") {
		t.Errorf("warning missing supervisor note: %q", out.String())
	}
}

// TestDaemonRestartSkipsStartWhenSupervised guards the restart branch: when a
// supervisor owns qid, restart must not attempt its own start. A deliberately
// bogus --qid-bin would surface any start attempt as a not-found error, so
// restart returning nil (with no "qid started") proves start was skipped.
func TestDaemonRestartSkipsStartWhenSupervised(t *testing.T) {
	sock := shortSock(t)
	startStoppable(t, sock)
	stubSupervisor(t, "com.olddognewflex.qid", true, true)

	env := &daemonEnv{socket: sock, qidBin: filepath.Join(t.TempDir(), "nonexistent-qid")}
	var out bytes.Buffer
	if err := env.restart(context.Background(), &out); err != nil {
		t.Fatalf("restart must not fail (nor attempt a start) under a supervisor: %v", err)
	}
	if strings.Contains(out.String(), "qid started") {
		t.Errorf("restart should not have started qid: %q", out.String())
	}
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
	if _, err := env.stop(context.Background(), &out); err != nil {
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
