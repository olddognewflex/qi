package agentrt

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Herdr environment variables, set inside every Herdr-managed pane.
const (
	envHerdrActive     = "HERDR_ENV"         // "1" inside a Herdr pane
	envHerdrBinPath    = "HERDR_BIN_PATH"    // absolute path to the herdr binary
	envHerdrSocketPath = "HERDR_SOCKET_PATH" // server socket
)

// statFunc is os.Stat, injected for tests.
type statFunc func(string) (fs.FileInfo, error)

// Detect picks the runtime for the current environment. env and lookPath
// are injected so the decision is pure and testable.
func Detect(env func(string) string, lookPath func(string) (string, error)) Runtime {
	return detect(env, lookPath, os.Stat)
}

// DetectDefault is Detect against the real process environment.
func DetectDefault() Runtime {
	return detect(os.Getenv, exec.LookPath, os.Stat)
}

// detect resolves the runtime in two steps.
//
// First, HERDR_ENV=1 means qi is running inside a Herdr pane: that is
// conclusive, and HERDR_BIN_PATH names the exact binary managing this
// session, so a PATH lookup would only risk picking a different build.
//
// Second, qi may be invoked from a plain terminal (a voice entry point, a
// cron job) while Herdr is running anyway. A herdr binary on PATH alone is
// not evidence of that — the binary is installed whether or not a server
// is up — so a live-server signal is also required: HERDR_SOCKET_PATH, or
// the socket file at its default location.
//
// Anything else falls back to LocalRuntime, which is honest about knowing
// no agents.
func detect(env func(string) string, lookPath func(string) (string, error), stat statFunc) Runtime {
	if env(envHerdrActive) == "1" {
		return NewHerdrRuntime(env(envHerdrBinPath))
	}
	bin, err := lookPath("herdr")
	if err != nil || bin == "" {
		return NewLocalRuntime()
	}
	if env(envHerdrSocketPath) != "" {
		return NewHerdrRuntime(bin)
	}
	if sock := defaultSocketPath(env); sock != "" {
		if _, err := stat(sock); err == nil {
			return NewHerdrRuntime(bin)
		}
	}
	return NewLocalRuntime()
}

// defaultSocketPath is where Herdr puts its socket when HERDR_SOCKET_PATH
// is unset: $XDG_CONFIG_HOME/herdr/herdr.sock, else ~/.config/herdr/herdr.sock.
func defaultSocketPath(env func(string) string) string {
	if dir := env("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "herdr", "herdr.sock")
	}
	if home := env("HOME"); home != "" {
		return filepath.Join(home, ".config", "herdr", "herdr.sock")
	}
	return ""
}

// ForName resolves an explicit runtime choice from configuration.
// "herdr" forces the Herdr adapter (bin overriding the binary, "" meaning
// "herdr"); "local" forces the no-agent fallback; "auto" — and an unset
// value, so a missing config key behaves like the default — runs Detect.
func ForName(name, bin string) (Runtime, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "auto":
		return DetectDefault(), nil
	case "herdr":
		return NewHerdrRuntime(bin), nil
	case "local":
		return NewLocalRuntime(), nil
	}
	return nil, fmt.Errorf("unknown agent runtime %q (want auto, herdr or local)", name)
}
