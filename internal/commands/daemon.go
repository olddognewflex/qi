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
			return env.stop(cmd.Context(), cmd.OutOrStdout())
		},
	})
	root.AddCommand(&cobra.Command{
		Use:          "restart",
		Short:        "Stop qid if running, then start it again",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := env.stop(cmd.Context(), cmd.OutOrStdout()); err != nil {
				return err
			}
			return env.start(cmd.Context(), cmd.OutOrStdout())
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
// an error rather than signalled or hunted down by name.
func (e *daemonEnv) stop(ctx context.Context, out io.Writer) error {
	sock, err := e.socketPath()
	if err != nil {
		return err
	}
	if !daemon.Alive(sock) {
		fmt.Fprintf(out, "qid not running (socket %s)\n", sock)
		return nil
	}
	cl, err := client.Dial(sock, dialTimeout)
	if err != nil {
		return err
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
			return fmt.Errorf("running qid predates the daemon.shutdown RPC; stop that process manually, then run 'qi daemon start'")
		}
		return fmt.Errorf("shutdown: %w", err)
	}

	deadline := time.Now().Add(daemonStopTimeout)
	if !daemon.WaitGone(sock, time.Until(deadline)) {
		return fmt.Errorf("qid still answering on %s after %s", sock, daemonStopTimeout)
	}
	if err := waitProcessGone(pid, time.Until(deadline)); err != nil {
		return err
	}
	fmt.Fprintln(out, "qid stopped")
	return nil
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
