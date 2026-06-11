package service

import (
	"fmt"
	"regexp"
	"strings"
)

// projectTagRe is the charset a free-form project tag may use. It matches the
// vault tag charset (see vault.tagRe) so the tag round-trips and the derived
// filename (projectFileName flattens "/" to "-") cannot traverse directories.
var projectTagRe = regexp.MustCompile(`^[A-Za-z0-9_\-/]+$`)

// ValidateAddInput is the single trust boundary every task crosses on its way
// into the vault. It is called from the remote-drain path (untrusted cloud
// input) and is available to any other writer. It checks the raw, pre-routing
// fields: free-form text, an optional project tag, and an optional client name.
// project and client are mutually exclusive; a client name must be configured.
//
// isClient reports whether a name is a configured client; callers pass a
// closure over Config.ClientByName. When isClient is nil any non-empty client
// name is rejected.
//
// Due/Scheduled dates are checked as already-parsed *time.Time on the input, so
// nothing date-related is validated here — string parsing happens in the
// caller (the drain layer), which surfaces a malformed date as a rejection.
func ValidateAddInput(input AddTaskInput, project, client string, isClient func(string) bool) error {
	if strings.TrimSpace(input.Text) == "" {
		return fmt.Errorf("text is empty")
	}
	// Reject control characters in text. A task line is a SINGLE markdown line;
	// an embedded newline (or other control char) from an untrusted remote
	// caller would forge additional task/markdown lines in the canonical vault
	// file. FormatTaskLine only trims edges, so this is where invariant #1 is
	// defended.
	if hasControlChar(input.Text) {
		return fmt.Errorf("text contains control characters")
	}

	project = strings.TrimSpace(project)
	client = strings.TrimSpace(client)
	if project != "" && client != "" {
		return fmt.Errorf("project and client are mutually exclusive")
	}

	if project != "" && !projectTagRe.MatchString(project) {
		// project becomes a #tag and a filename; constrain it to the tag charset
		// so it can neither break the line nor escape the tasks dir.
		return fmt.Errorf("invalid project %q (allowed: letters, digits, _-/)", project)
	}

	if client != "" {
		if isClient == nil || !isClient(client) {
			return fmt.Errorf("unknown client %q", client)
		}
	}

	return nil
}

// hasControlChar reports whether s contains any ASCII control character
// (including CR/LF and tab). Task text must stay on one markdown line.
func hasControlChar(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
