package daemon

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// QidBinEnv overrides which qid binary the lifecycle commands spawn.
const QidBinEnv = "QI_QID_BIN"

// dialProbe bounds a liveness dial. The socket is local; anything slower than
// this is a wedged peer, not a slow one.
const dialProbe = 200 * time.Millisecond

// ResolveQidBinary locates the qid binary to run, in order: the explicit flag,
// $QI_QID_BIN, a qid sitting next to the running qi, then $PATH. The sibling
// lookup comes before $PATH so `qi daemon restart` starts the qid built in the
// same install step rather than an older one earlier on the path — the whole
// point of the restart command.
func ResolveQidBinary(flag string) (string, error) {
	exeDir := ""
	if exe, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exe)
	}
	return resolveQidBinary(flag, os.Getenv(QidBinEnv), exeDir, isExecutableFile, exec.LookPath)
}

// resolveQidBinary is ResolveQidBinary with its three environment lookups
// injected so every branch is testable without touching the real filesystem.
func resolveQidBinary(flag, env, exeDir string, executable func(string) bool, lookPath func(string) (string, error)) (string, error) {
	explicit := []struct{ value, source string }{
		{flag, "--qid-bin"},
		{env, QidBinEnv},
	}
	for _, e := range explicit {
		if e.value == "" {
			continue
		}
		if !executable(e.value) {
			return "", fmt.Errorf("%s %q is not an executable file", e.source, e.value)
		}
		return e.value, nil
	}
	if exeDir != "" {
		sibling := filepath.Join(exeDir, qidBinName())
		if executable(sibling) {
			return sibling, nil
		}
	}
	found, err := lookPath(qidBinName())
	if err != nil {
		return "", fmt.Errorf("qid binary not found: pass --qid-bin, set %s, or put qid on PATH", QidBinEnv)
	}
	return found, nil
}

// qidBinName is the qid executable's file name. The sibling lookup joins a
// path itself, so it needs the extension on Windows; exec.LookPath adds one on
// its own.
func qidBinName() string {
	if runtime.GOOS == "windows" {
		return "qid.exe"
	}
	return "qid"
}

func isExecutableFile(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return fi.Mode().Perm()&0o111 != 0
}

// Staleness reports whether a running daemon is executing the qid binary
// currently on disk.
type Staleness int

const (
	// StalenessUnknown means one of the two timestamps is missing — a daemon
	// that predates started_at reporting, or a binary that cannot be located.
	// Unknown is reported as unknown; it is never guessed either way.
	StalenessUnknown Staleness = iota
	StalenessCurrent
	StalenessStale
)

func (s Staleness) String() string {
	switch s {
	case StalenessCurrent:
		return "current"
	case StalenessStale:
		return "stale"
	default:
		return "unknown"
	}
}

// BinaryStaleness compares the on-disk qid binary's mtime against the running
// daemon's start time: a binary written after the daemon started means the
// daemon is serving code that no longer exists on disk. A zero timestamp on
// either side yields StalenessUnknown. Pure — no I/O — so the decision is
// testable without spawning anything.
func BinaryStaleness(binModTime, startedAt time.Time) Staleness {
	if binModTime.IsZero() || startedAt.IsZero() {
		return StalenessUnknown
	}
	if binModTime.After(startedAt) {
		return StalenessStale
	}
	return StalenessCurrent
}

// StalenessOf combines the two independent ways a running daemon can be out of
// date: it is executing a different binary than the one `qi daemon restart`
// would start (a path mismatch — say a daemon launched from /usr/local/bin
// while the fresh build landed in ~/.local/bin), or it is executing the right
// path but that file has been rewritten since it started. The path mismatch is
// checked first because no mtime comparison can reveal it. Both paths must
// already be resolved (symlinks followed) by the caller; empty means unknown,
// which skips the mismatch check. Pure — no I/O.
func StalenessOf(daemonExe, resolvedExe string, binModTime, startedAt time.Time) (verdict Staleness, mismatched bool) {
	if daemonExe != "" && resolvedExe != "" && daemonExe != resolvedExe {
		return StalenessStale, true
	}
	return BinaryStaleness(binModTime, startedAt), false
}

// Alive reports whether anything accepts connections on the socket. A dial
// only proves the kernel accepted from the listen backlog, not that qid is
// serving — use the status RPC when readiness matters.
func Alive(path string) bool {
	conn, err := net.DialTimeout("unix", path, dialProbe)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// WaitGone polls until nothing answers on the socket, reporting false if the
// daemon is still reachable when timeout elapses.
func WaitGone(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !Alive(path) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// LogPath is the file `qi daemon start` appends a spawned qid's stdout and
// stderr to: a sibling of the socket, so it lives with the rest of qi's
// machine-local runtime state.
func LogPath(socketPath string) string {
	return filepath.Join(filepath.Dir(socketPath), "qid.log")
}
