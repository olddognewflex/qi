//go:build !darwin

package commands

import "context"

// qidSupervisor cannot introspect a launchd domain off darwin. It reports
// known=false so the caller falls back to generic respawn detection, which also
// covers other supervisors (systemd Restart=always, etc.) without qi needing to
// know anything about them.
func qidSupervisor(_ context.Context) (label string, supervised, known bool) {
	return "", false, false
}
