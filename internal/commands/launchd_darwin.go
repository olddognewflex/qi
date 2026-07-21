//go:build darwin

package commands

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// qidSupervisor reports whether a launchd agent that manages qid is loaded in
// the caller's launchd domain, and its label. It shells out to `launchctl list`
// (cheap, no privilege) and matches any loaded label containing "qid" — the
// shipped agent is com.olddognewflex.qid, and a stale com.odnf.qid has been seen
// on the same box (issue #69). `known` is false only when launchctl itself could
// not be consulted, in which case the caller falls back to respawn detection.
//
// This is the reliable signal: launchd's relaunch is timing-variable (near-
// instant, but up to ThrottleInterval ~10s when qid is killed soon after it
// started), so a fixed respawn-poll window can miss it — the loaded-agent check
// cannot.
func qidSupervisor(ctx context.Context) (label string, supervised, known bool) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "launchctl", "list").Output()
	if err != nil {
		return "", false, false
	}
	// `launchctl list` prints "PID<TAB>Status<TAB>Label" rows; the label is the
	// last field.
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		lbl := fields[len(fields)-1]
		if strings.Contains(strings.ToLower(lbl), "qid") {
			return lbl, true, true
		}
	}
	return "", false, true
}
