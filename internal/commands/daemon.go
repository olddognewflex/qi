package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"qi/internal/daemon"
	"qi/internal/daemon/client"
)

// Timeouts for the lifecycle round-trips. Vars, not consts, so tests can
// shorten the waits without sleeping through the real budgets.
var (
	daemonStartTimeout  = 5 * time.Second
	daemonStopTimeout   = 5 * time.Second
	daemonStatusTimeout = 2 * time.Second
	// supervisorRecheckWindow is how long `stop` watches for qid to come back
	// after a clean shutdown — the signature of a process supervisor (a launchd
	// agent with KeepAlive, systemd Restart=always) that owns qid's lifecycle
	// and relaunched it. Without this, `stop` truthfully reports "qid stopped"
	// for the instant the socket is quiet, then the supervisor respawns qid a
	// beat later and the report is a lie (issue #69). launchd typically
	// relaunches within ~1s; a throttled agent past this window is missed and
	// still reported stopped.
	supervisorRecheckWindow = 2 * time.Second
)

// daemonEnv holds the flags shared by the daemon subcommands. The subcommands
// are built before the flags parse, so they close over this struct rather than
// over the values.
type daemonEnv struct {
	socket string
	qidBin string
}

func newDaemonCommand() *cobra.Command {
	env := &daemonEnv{}
	root := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the qid daemon (status, start, stop, restart)",
		Long: "Starts, stops, restarts, and inspects qid. The qid binary is resolved\n" +
			"sibling-first (next to this qi, before $PATH) so a rebuilt qi restarts the\n" +
			"qid it was built with. Works without a vault config.",
	}
	root.PersistentFlags().StringVar(&env.socket, "socket", "", "qid socket path override")
	root.PersistentFlags().StringVar(&env.qidBin, "qid-bin", "", "qid binary to run (default: sibling of qi, else PATH)")

	root.AddCommand(&cobra.Command{
		Use:          "status",
		Short:        "Report whether qid is running, and whether its binary is stale",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return env.status(cmd.Context(), cmd.OutOrStdout())
		},
	})
	root.AddCommand(&cobra.Command{
		Use:          "start",
		Short:        "Start qid in the background (no-op if already running)",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return env.start(cmd.Context(), cmd.OutOrStdout())
		},
	})
	root.AddCommand(&cobra.Command{
		Use:          "stop",
		Short:        "Stop qid gracefully (no-op if not running)",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := env.stop(cmd.Context(), cmd.OutOrStdout())
			return err
		},
	})
	root.AddCommand(&cobra.Command{
		Use:          "restart",
		Short:        "Stop qid if running, then start it again",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return env.restart(cmd.Context(), cmd.OutOrStdout())
		},
	})
	return root
}

func (e *daemonEnv) socketPath() (string, error) {
	if e.socket != "" {
		return e.socket, nil
	}
	return daemon.SocketPath()
}

// status prints the daemon's liveness and, when it is running, its binary,
// start time, watcher state, and staleness. It never fails on "not running":
// this reports state, it does not assert it.
func (e *daemonEnv) status(ctx context.Context, out io.Writer) error {
	sock, err := e.socketPath()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "socket:    %s\n", sock)

	if !daemon.Alive(sock) {
		fmt.Fprintln(out, "state:     not running")
		if bin, err := daemon.ResolveQidBinary(e.qidBin); err == nil {
			fmt.Fprintf(out, "qid bin:   %s\n", bin)
		}
		fmt.Fprintln(out, "\nstart it with 'qi daemon start'")
		return nil
	}
	fmt.Fprintln(out, "state:     running")

	cl, derr := client.Dial(sock, dialTimeout)
	if derr != nil {
		fmt.Fprintf(out, "status:    unknown (%v)\n", derr)
		return nil
	}
	defer cl.Close()

	callCtx, cancel := context.WithTimeout(ctx, daemonStatusTimeout)
	defer cancel()
	st, serr := cl.Status(callCtx)
	if serr != nil {
		var ce *client.CallError
		if errors.As(serr, &ce) && ce.Code == daemon.CodeMethodNotFound {
			fmt.Fprintln(out, "status:    unknown (daemon predates the status RPC; restart it)")
			return nil
		}
		fmt.Fprintf(out, "status:    unknown (%v)\n", serr)
		return nil
	}

	fmt.Fprintf(out, "exe:       %s\n", orUnknown(st.Exe))
	if st.Pid > 0 {
		fmt.Fprintf(out, "pid:       %d\n", st.Pid)
	} else {
		fmt.Fprintln(out, "pid:       unknown (daemon predates pid reporting)")
	}
	if st.StartedAt.IsZero() {
		fmt.Fprintln(out, "started:   unknown")
	} else {
		fmt.Fprintf(out, "started:   %s (up %s)\n",
			st.StartedAt.Format(time.RFC3339), time.Since(st.StartedAt).Round(time.Second))
	}
	fmt.Fprintf(out, "watcher:   %s\n", watcherSummary(st))
	_, staleSummary := daemonStaleness(st.Exe, e.qidBin, st.StartedAt)
	fmt.Fprintf(out, "binary:    %s\n", staleSummary)
	return nil
}

// restart stops qid and starts it again. If a supervisor relaunched qid during
// the stop (respawned), it returns without starting: restart can neither keep a
// supervised daemon down nor choose its binary, and racing a second start would
// just print a misleading "already running". stop has already explained the
// situation and pointed at the launchctl remedy.
func (e *daemonEnv) restart(ctx context.Context, out io.Writer) error {
	respawned, err := e.stop(ctx, out)
	if err != nil {
		return err
	}
	if respawned {
		return nil
	}
	return e.start(ctx, out)
}

// start spawns qid detached and waits until it answers the status RPC. It is
// idempotent: an already-running daemon is reported and left alone.
func (e *daemonEnv) start(ctx context.Context, out io.Writer) error {
	sock, err := e.socketPath()
	if err != nil {
		return err
	}
	if daemon.Alive(sock) {
		fmt.Fprintf(out, "qid already running (socket %s)\n", sock)
		return nil
	}
	bin, err := daemon.ResolveQidBinary(e.qidBin)
	if err != nil {
		return err
	}

	logPath := daemon.LogPath(sock)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return fmt.Errorf("create runtime dir: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", logPath, err)
	}
	defer logFile.Close()

	var args []string
	if e.socket != "" {
		args = append(args, "--socket", e.socket)
	}
	c := exec.Command(bin, args...)
	c.Stdout, c.Stderr = logFile, logFile
	detach(c)
	if err := c.Start(); err != nil {
		return fmt.Errorf("start %s: %w", bin, err)
	}
	pid := c.Process.Pid

	// Watch for an early exit alongside the readiness poll. qid loads its own
	// config and quits immediately when no vault is set — and `qi daemon` runs
	// without a config on purpose — so waiting the full readiness timeout for a
	// generic message would hide the real, instantly-known reason. Reaping in a
	// goroutine does not tether qid to qi: this process exits either way.
	exited := make(chan error, 1)
	go func() { exited <- c.Wait() }()

	pollCtx, stopPoll := context.WithCancel(ctx)
	defer stopPoll()
	budget := daemonStartTimeout
	ready := make(chan error, 1)
	go func() {
		_, err := client.WaitReady(pollCtx, sock, budget)
		ready <- err
	}()

	select {
	case werr := <-exited:
		return fmt.Errorf("qid exited immediately: %v (see %s)", exitReason(werr), logPath)
	case err := <-ready:
		if err != nil {
			// Never leave a half-started daemon behind: it holds no usable
			// socket but would block the next start's stale-socket probe.
			_ = c.Process.Kill()
			return fmt.Errorf("qid (pid %d) did not become ready: %w (see %s)", pid, err, logPath)
		}
	}
	fmt.Fprintf(out, "qid started (pid %d, socket %s, log %s)\n", pid, sock, logPath)
	return nil
}

// exitReason renders what Wait reported. A clean exit still means the daemon is
// gone, which is a failure here, so nil gets its own wording.
func exitReason(err error) string {
	if err == nil {
		return "exit status 0"
	}
	return err.Error()
}

// stop asks the running daemon to shut down and waits for the socket to go
// quiet. There is no pid file, so a daemon that ignores the RPC is reported as
// an error rather than signalled or hunted down by name. It returns whether a
// process supervisor relaunched qid within supervisorRecheckWindow — in which
// case the stop did not stick and stop has said so.
func (e *daemonEnv) stop(ctx context.Context, out io.Writer) (respawned bool, err error) {
	sock, err := e.socketPath()
	if err != nil {
		return false, err
	}
	if !daemon.Alive(sock) {
		fmt.Fprintf(out, "qid not running (socket %s)\n", sock)
		return false, nil
	}
	cl, err := client.Dial(sock, dialTimeout)
	if err != nil {
		return false, err
	}
	defer cl.Close()

	// Ask who we are stopping before stopping it: the socket going quiet only
	// proves the listener closed, and Serve still has to drain in-flight
	// handlers. A wedged handler (a hung MCP call, a blocked iCloud read) leaves
	// the process running behind a closed listener, which would otherwise be
	// reported as a clean stop.
	statusCtx, statusCancel := context.WithTimeout(ctx, daemonStatusTimeout)
	pid := 0
	if st, serr := cl.Status(statusCtx); serr == nil {
		pid = st.Pid
	}
	statusCancel()

	callCtx, cancel := context.WithTimeout(ctx, daemonStopTimeout)
	defer cancel()
	if err := cl.Shutdown(callCtx); err != nil {
		var ce *client.CallError
		if errors.As(err, &ce) && ce.Code == daemon.CodeMethodNotFound {
			return false, fmt.Errorf("running qid predates the daemon.shutdown RPC; stop that process manually, then run 'qi daemon start'")
		}
		return false, fmt.Errorf("shutdown: %w", err)
	}

	deadline := time.Now().Add(daemonStopTimeout)
	if !daemon.WaitGone(sock, time.Until(deadline)) {
		return false, fmt.Errorf("qid still answering on %s after %s", sock, daemonStopTimeout)
	}
	if err := waitProcessGone(pid, time.Until(deadline)); err != nil {
		return false, err
	}

	// The socket is quiet and the process is gone — but a process supervisor
	// (launchd KeepAlive, systemd Restart=always) notices qid died and relaunches
	// it. Reporting "qid stopped" while a supervisor brings it back is the trap
	// this guards against (issue #69).
	//
	// Prefer asking the supervisor directly: a loaded launchd agent is a
	// timing-independent signal, where a respawn-poll can miss a throttled
	// relaunch (launchd's ThrottleInterval can delay it up to ~10s). Fall back to
	// watching for the respawn only when we cannot introspect the supervisor
	// (non-macOS, or launchctl unavailable) — which also covers systemd et al.
	if supervisorProbeMatchesSocket(sock) {
		if label, supervised, known := supervisorProbe(ctx); known {
			if supervised {
				fmt.Fprint(out, supervisorRelaunchWarning(label))
				return true, nil
			}
			fmt.Fprintln(out, "qid stopped")
			return false, nil
		}
	}
	if waitRespawn(sock, supervisorRecheckWindow) {
		fmt.Fprint(out, supervisorRelaunchWarning(""))
		return true, nil
	}
	fmt.Fprintln(out, "qid stopped")
	return false, nil
}

// supervisorProbe detects a launchd-style supervisor for qid. A package var so
// tests can stub out the real launchctl call — which, run in-process, would
// otherwise see the developer's own loaded qid agent and contaminate every stop
// test.
var supervisorProbe = qidSupervisor

// supervisorProbeMatchesSocket limits the launchd label check to the canonical
// qid socket. A loaded default qid agent says nothing about a separate daemon
// started with --socket; an actual supervisor for that custom socket is still
// detected by the socket-specific respawn poll below.
func supervisorProbeMatchesSocket(sock string) bool {
	defaultSock, err := daemon.SocketPath()
	return err == nil && filepath.Clean(sock) == filepath.Clean(defaultSock)
}

// waitRespawn reports whether qid becomes reachable again within budget after a
// clean stop — the observable signature of a supervisor that owns qid's
// lifecycle. Supervisor-agnostic fallback for when supervisorProbe cannot
// introspect the supervisor. Returns false if the socket stays quiet for the
// whole window.
func waitRespawn(sock string, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for {
		if daemon.Alive(sock) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// supervisorRelaunchWarning is what `stop`/`restart` print when a supervisor
// owns qid. When the launchd label is known it is spliced into the remedy
// commands; otherwise the user is told how to find it.
func supervisorRelaunchWarning(label string) string {
	if label == "" {
		label = "<label>  # find it: launchctl list | grep qid"
	}
	return fmt.Sprintf(`warning: a process supervisor owns qid's lifecycle and relaunches it, so
         'qi daemon stop' cannot keep it down and 'qi daemon restart' cannot
         choose its binary. On macOS this is a launchd agent (KeepAlive):
           keep it down:     launchctl bootout gui/%[1]d/%[2]s
           restart it fresh: launchctl kickstart -k gui/%[1]d/%[2]s
`, os.Getuid(), label)
}

// waitProcessGone polls for the daemon process to actually exit after its
// socket went quiet. An unknown pid (an older daemon, or a platform where
// liveness is not observable) keeps the socket-only semantics. It never kills
// the process and never shells out to look one up by name — with no pid file,
// the reported pid is the only identity we can trust.
func waitProcessGone(pid int, budget time.Duration) error {
	if alive, known := processAlive(pid); !known || !alive {
		return nil
	}
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		if alive, known := processAlive(pid); !known || !alive {
			return nil
		}
	}
	return fmt.Errorf("qid closed its socket but process %d is still running "+
		"(a tool call is likely still draining); wait, or stop it manually", pid)
}

// daemonStaleness decides whether a running daemon is executing the qid binary
// `qi daemon restart` would start, and renders the verdict. Two independent
// ways it can be out of date, both covered by daemon.StalenessOf: a path
// mismatch (the daemon runs /usr/local/bin/qid while the fresh build is the
// ~/.local/bin/qid qi resolves — no mtime comparison can see this), or the
// right path rewritten since the daemon started. The daemon's own reported exe
// is preferred, being the only path guaranteed to be the one it is executing;
// with none reported, the resolved binary is the best available stand-in.
// Shared by `qi daemon status` and `qi doctor` so both describe staleness
// identically.
func daemonStaleness(reportedExe, qidBinFlag string, startedAt time.Time) (daemon.Staleness, string) {
	resolved, resolveErr := daemon.ResolveQidBinary(qidBinFlag)
	exe := reportedExe
	if exe == "" {
		if resolveErr != nil {
			return daemon.StalenessUnknown, "unknown (daemon did not report its binary and qid could not be located)"
		}
		exe = resolved
	}
	fi, err := os.Stat(exe)
	if err != nil {
		return daemon.StalenessUnknown, fmt.Sprintf("unknown (cannot stat %s: %v)", exe, err)
	}

	// Compare through symlinks so a symlinked install path is not mistaken for
	// a different binary. An unresolvable path compares as-is rather than
	// suppressing the check.
	daemonExe, resolvedExe := realPath(reportedExe), ""
	if resolveErr == nil {
		resolvedExe = realPath(resolved)
	}

	verdict, mismatched := daemon.StalenessOf(daemonExe, resolvedExe, fi.ModTime(), startedAt)
	switch {
	case mismatched:
		return verdict, fmt.Sprintf("stale — running %s but 'qi daemon restart' would start %s", reportedExe, resolved)
	case verdict == daemon.StalenessStale:
		return verdict, "stale — binary rebuilt after daemon start; run 'qi daemon restart'"
	case verdict == daemon.StalenessCurrent:
		return verdict, fmt.Sprintf("current (%s)", exe)
	default:
		return daemon.StalenessUnknown, "unknown (daemon does not report its start time; restart it to enable this check)"
	}
}

// realPath resolves symlinks, falling back to the input when it cannot.
func realPath(path string) string {
	if path == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

func watcherSummary(st client.DaemonStatus) string {
	if st.Watcher.State == "" {
		return "unknown"
	}
	if st.Watcher.Detail == "" {
		return st.Watcher.State
	}
	return st.Watcher.State + " — " + st.Watcher.Detail
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown (daemon predates exe reporting)"
	}
	return s
}
