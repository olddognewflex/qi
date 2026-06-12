// Package notify implements qid's opt-in, read-only morning due-today notifier.
// On macOS it sends a native banner via osascript; on other platforms it falls
// back to logging the notification. It is independent of the service/domain
// layers: the Notifier takes plain strings and the scheduler takes a plain
// callback, so this package imports only the standard library.
package notify

import (
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"strings"
)

// Notifier delivers a single notification with the given title and body.
type Notifier interface {
	Notify(title, body string) error
}

// NewNotifier returns the platform notifier: an osascript-backed banner on
// macOS, otherwise a logging fallback that records the notification at Info.
func NewNotifier(log *slog.Logger) Notifier {
	if log == nil {
		log = slog.Default()
	}
	if runtime.GOOS == "darwin" {
		return osaNotifier{}
	}
	return logNotifier{log: log}
}

// osaNotifier sends a macOS banner via `osascript -e`.
type osaNotifier struct{}

func (osaNotifier) Notify(title, body string) error {
	script := buildAppleScript(title, body)
	if err := exec.Command("osascript", "-e", script).Run(); err != nil {
		return fmt.Errorf("notify: osascript: %w", err)
	}
	return nil
}

// logNotifier is the non-darwin fallback: it logs the notification and returns
// nil so the scheduler treats delivery as successful.
type logNotifier struct {
	log *slog.Logger
}

func (n logNotifier) Notify(title, body string) error {
	n.log.Info("notification", "title", title, "body", body)
	return nil
}

// buildAppleScript builds the AppleScript passed to `osascript -e`. Title and
// body are wrapped in AppleScript string literals whose contents are escaped so
// embedded quotes/backslashes/newlines cannot break the script or inject
// additional AppleScript.
func buildAppleScript(title, body string) string {
	return fmt.Sprintf("display notification \"%s\" with title \"%s\"", appleEscape(body), appleEscape(title))
}

// appleEscape sanitises a string for use inside an AppleScript double-quoted
// literal: backslashes and double-quotes are escaped, and newlines/tabs are
// collapsed to spaces (macOS notifications render them as spaces anyway, and it
// keeps the one-line script intact).
func appleEscape(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n', '\r', '\t':
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
