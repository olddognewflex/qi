package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qi/internal/daemon"
	daemonclient "qi/internal/daemon/client"
	"qi/internal/policy"
	"qi/internal/tools"
)

// The spawn path can only be exercised by actually spawning something, so the
// test binary doubles as a stand-in qid: TestMain re-enters fakeQidMain when
// QI_FAKE_QID_SOCKET is set, and the spawn tests point --qid-bin at
// os.Args[0]. The fake speaks the real JSON-RPC server with the real status and
// shutdown methods, so `start`, readiness polling, `stop`, and `restart` run
// against genuine wire behavior rather than a mock.
const (
	fakeQidSocketEnv = "QI_FAKE_QID_SOCKET"
	fakeQidModeEnv   = "QI_FAKE_QID_MODE"
)

func TestMain(m *testing.M) {
	if sock := os.Getenv(fakeQidSocketEnv); sock != "" {
		fakeQidMain(sock)
		return
	}
	os.Exit(m.Run())
}

func fakeQidMain(sock string) {
	switch os.Getenv(fakeQidModeEnv) {
	case "exit":
		// Mirrors the real qid failing config.Load() and quitting at once.
		fmt.Fprintln(os.Stderr, "fake qid: vault_path is not configured")
		os.Exit(1)
	case "silent":
		// Never binds: forces the readiness timeout and the kill that follows.
		// A plain block would trip the runtime's deadlock detector and exit.
		time.Sleep(time.Hour)
	}

	ln, err := daemon.Listen(sock)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake qid: listen:", err)
		os.Exit(1)
	}
	defer ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := daemon.NewServer(tools.NewRegistry(), policy.DefaultDecider{}, nil, log)
	exe, _ := os.Executable()
	startedAt := time.Now()
	srv.Register("status", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.Marshal(daemonclient.DaemonStatus{
			Watcher:   daemonclient.WatcherStatus{State: "disabled"},
			Exe:       exe,
			Pid:       os.Getpid(),
			StartedAt: startedAt,
		})
	})
	srv.Register("daemon.shutdown", func(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
		var p struct {
			Caller string `json:"caller"`
		}
		_ = json.Unmarshal(raw, &p)
		if p.Caller != policy.CallerCLI {
			return nil, fmt.Errorf("daemon.shutdown is restricted to the cli caller (got %q)", p.Caller)
		}
		go func() {
			time.Sleep(20 * time.Millisecond)
			cancel()
		}()
		return json.RawMessage(`{"status":"stopping"}`), nil
	})

	fmt.Println("fake qid listening on", sock)
	_ = srv.Serve(ctx, ln)

	if os.Getenv(fakeQidModeEnv) == "linger" {
		// Deliberately hostile teardown: keep running well after the listener
		// closed (the real qid waits on MCP subprocesses and the audit log
		// there), then unlink the socket path on the way out. A stop that
		// declares success on socket silence alone hands this predecessor the
		// chance to delete its successor's socket.
		time.Sleep(400 * time.Millisecond)
		_ = os.Remove(sock)
	}
	os.Exit(0)
}

// spawnEnv wires a daemonEnv whose "qid" is this test binary. Socket paths stay
// under /tmp rather than t.TempDir() because macOS caps sun_path at 104 bytes
// and the default temp dir alone can exceed it.
func spawnEnv(t *testing.T, mode string) *daemonEnv {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "qid-spawn")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	sock := filepath.Join(dir, "qid.sock")
	t.Setenv(fakeQidSocketEnv, sock)
	t.Setenv(fakeQidModeEnv, mode)
	t.Cleanup(func() {
		env := &daemonEnv{socket: sock, qidBin: os.Args[0]}
		_ = env.stop(context.Background(), io.Discard)
		_ = os.RemoveAll(dir)
	})
	return &daemonEnv{socket: sock, qidBin: os.Args[0]}
}

func shortenDaemonTimeouts(t *testing.T, d time.Duration) {
	t.Helper()
	start, stop, status := daemonStartTimeout, daemonStopTimeout, daemonStatusTimeout
	daemonStartTimeout, daemonStopTimeout, daemonStatusTimeout = d, d, d
	t.Cleanup(func() {
		daemonStartTimeout, daemonStopTimeout, daemonStatusTimeout = start, stop, status
	})
}

func TestDaemonStartStopRoundTrip(t *testing.T) {
	env := spawnEnv(t, "")
	var out bytes.Buffer

	if err := env.start(context.Background(), &out); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !strings.Contains(out.String(), "qid started (pid ") {
		t.Fatalf("start output = %q", out.String())
	}
	if !daemon.Alive(env.socket) {
		t.Fatal("socket not answering after start")
	}

	// The log file must exist, be 0600, and hold the daemon's own stdout.
	logPath := daemon.LogPath(env.socket)
	fi, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat log: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("log perms = %v, want 0600", perm)
	}
	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(body), "fake qid listening") {
		t.Errorf("log does not capture daemon stdout: %q", body)
	}

	// Status must report the live daemon's own pid and exe.
	out.Reset()
	if err := env.status(context.Background(), &out); err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, want := range []string{"state:     running", "pid:", "exe:"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status output %q missing %q", out.String(), want)
		}
	}

	// A second start is a no-op, not a second daemon.
	out.Reset()
	if err := env.start(context.Background(), &out); err != nil {
		t.Fatalf("second start: %v", err)
	}
	if !strings.Contains(out.String(), "already running") {
		t.Fatalf("second start output = %q", out.String())
	}

	out.Reset()
	if err := env.stop(context.Background(), &out); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !strings.Contains(out.String(), "qid stopped") {
		t.Fatalf("stop output = %q", out.String())
	}
	if daemon.Alive(env.socket) {
		t.Fatal("socket still answering after stop")
	}
	if _, err := os.Stat(env.socket); err == nil {
		t.Fatal("socket file survived shutdown; Close should have unlinked it")
	}
}

// TestDaemonRestartLeavesASurvivingDaemon is the regression for the teardown
// race: the outgoing daemon must not unlink the socket its successor has
// already bound. Restarting repeatedly widens the window — a lingering
// os.Remove in the old process's exit path shows up as a dead socket or a
// successor that never comes up.
func TestDaemonRestartLeavesASurvivingDaemon(t *testing.T) {
	env := spawnEnv(t, "")
	var out bytes.Buffer
	if err := env.start(context.Background(), &out); err != nil {
		t.Fatalf("start: %v", err)
	}

	for i := range 5 {
		out.Reset()
		if err := env.stop(context.Background(), &out); err != nil {
			t.Fatalf("restart %d stop: %v", i, err)
		}
		if err := env.start(context.Background(), &out); err != nil {
			t.Fatalf("restart %d start: %v", i, err)
		}
		// Give the outgoing process time to finish exiting, then confirm the
		// successor is still reachable on its own socket.
		time.Sleep(150 * time.Millisecond)
		if !daemon.Alive(env.socket) {
			t.Fatalf("restart %d: socket dead after the previous daemon exited", i)
		}
		st, err := daemonclient.WaitReady(context.Background(), env.socket, 2*time.Second)
		if err != nil {
			t.Fatalf("restart %d: successor not answering: %v", i, err)
		}
		if st.Pid == 0 {
			t.Fatalf("restart %d: successor reported no pid", i)
		}
	}
}

// TestDaemonRestartSurvivesLingeringPredecessor is the regression for the
// teardown race. The fake daemon keeps running after its listener closes and
// unlinks the socket path as it finally exits — the shape of the real qid's
// exit path. `stop` must therefore wait for the process, not merely for the
// socket to go quiet, or the successor `start` binds a socket the predecessor
// then deletes and the daemon becomes permanently unreachable.
func TestDaemonRestartSurvivesLingeringPredecessor(t *testing.T) {
	env := spawnEnv(t, "linger")
	var out bytes.Buffer
	if err := env.start(context.Background(), &out); err != nil {
		t.Fatalf("start: %v", err)
	}
	predecessor, err := daemonclient.WaitReady(context.Background(), env.socket, 2*time.Second)
	if err != nil {
		t.Fatalf("wait ready: %v", err)
	}

	if err := env.stop(context.Background(), &out); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if alive, known := processAlive(predecessor.Pid); known && alive {
		t.Fatal("stop returned while the daemon process was still running")
	}
	if err := env.start(context.Background(), &out); err != nil {
		t.Fatalf("restart start: %v", err)
	}

	// Well past the predecessor's linger window, the successor must still own a
	// live socket.
	time.Sleep(600 * time.Millisecond)
	if !daemon.Alive(env.socket) {
		t.Fatal("successor's socket was unlinked by the outgoing daemon")
	}
	st, err := daemonclient.WaitReady(context.Background(), env.socket, 2*time.Second)
	if err != nil {
		t.Fatalf("successor not answering: %v", err)
	}
	if st.Pid == predecessor.Pid {
		t.Fatal("restart did not actually replace the daemon")
	}
}

func TestDaemonStartReportsImmediateExit(t *testing.T) {
	shortenDaemonTimeouts(t, 3*time.Second)
	env := spawnEnv(t, "exit")

	start := time.Now()
	err := env.start(context.Background(), &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected an error when qid exits at once")
	}
	if !strings.Contains(err.Error(), "exited immediately") {
		t.Fatalf("err = %v, want an immediate-exit diagnosis", err)
	}
	// The whole point: report as soon as the child dies, not after the
	// readiness budget expires.
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("took %s to notice an instant exit", elapsed)
	}
	if !strings.Contains(err.Error(), daemon.LogPath(env.socket)) {
		t.Errorf("err = %v, should point at the log file", err)
	}
}

func TestDaemonStartKillsChildThatNeverBecomesReady(t *testing.T) {
	shortenDaemonTimeouts(t, 500*time.Millisecond)
	env := spawnEnv(t, "silent")

	err := env.start(context.Background(), &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected a readiness timeout")
	}
	if !strings.Contains(err.Error(), "did not become ready") {
		t.Fatalf("err = %v, want a readiness diagnosis", err)
	}
	pid := pidFromError(t, err)
	// The half-started child must not be left behind.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if alive, known := processAlive(pid); !known || !alive {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("orphaned qid pid %d still running after a failed start", pid)
}

// pidFromError plucks the pid out of "qid (pid 123) did not become ready: …".
func pidFromError(t *testing.T, err error) int {
	t.Helper()
	var pid int
	if _, serr := fmt.Sscanf(err.Error(), "qid (pid %d)", &pid); serr != nil {
		t.Fatalf("cannot read pid from %q: %v", err, serr)
	}
	return pid
}

func TestWaitProcessGone(t *testing.T) {
	t.Run("unknown pid keeps socket-only semantics", func(t *testing.T) {
		if err := waitProcessGone(0, time.Second); err != nil {
			t.Fatalf("pid 0 should be treated as unknown, got %v", err)
		}
	})

	t.Run("live process past the budget is reported, not killed", func(t *testing.T) {
		self := os.Getpid()
		err := waitProcessGone(self, 150*time.Millisecond)
		if err == nil {
			t.Fatal("expected a still-running report")
		}
		if !strings.Contains(err.Error(), "still running") {
			t.Fatalf("err = %v", err)
		}
		if alive, known := processAlive(self); known && !alive {
			t.Fatal("waitProcessGone must never kill the process it reports on")
		}
	})
}
