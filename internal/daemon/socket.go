package daemon

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// SocketPath returns the canonical path for qid's unix-domain socket.
// Honors XDG_RUNTIME_DIR, falling back to XDG_STATE_HOME, then
// ~/.local/state/qi/qid.sock.
func SocketPath() (string, error) {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "qi", "qid.sock"), nil
	}
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "qi", "qid.sock"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".local", "state", "qi", "qid.sock"), nil
}

// Listen creates a unix-domain listener at path with 0600 permissions. If the
// path already exists, it dials it once: a successful dial means another
// daemon is running and an error is returned; a failed dial means the socket
// file is stale, it is removed, and the listener is created.
func Listen(path string) (net.Listener, error) {
	// Fail with a clear message before net.Listen turns an over-length path into
	// a bare "bind: invalid argument" (#68). The limit is platform-specific
	// (sockaddr_un.sun_path: 103 usable on darwin, 107 elsewhere).
	if len(path) > maxSocketPathLen {
		return nil, fmt.Errorf("socket path is %d bytes, over the %d-byte OS limit for unix sockets on this platform; use a shorter --socket or XDG_RUNTIME_DIR", len(path), maxSocketPathLen)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir socket parent: %w", err)
	}

	if _, err := os.Stat(path); err == nil {
		if conn, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond); dialErr == nil {
			_ = conn.Close()
			return nil, fmt.Errorf("qid already running at %s", path)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("remove stale socket: %w", err)
		}
	}

	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen unix %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("chmod socket: %w", err)
	}
	return ln, nil
}
