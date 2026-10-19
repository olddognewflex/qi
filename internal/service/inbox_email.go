package service

import (
	"regexp"
	"strings"
)

// emailCaptureRE matches the one-line email summary captures an upstream mail
// filter writes into the inbox:
//
//	Email: <subject> — <sender> [<tag>]
//
// The sender may be empty ("Email: Hours reminder —  [read_now]"); the tag is
// matched case-insensitively and folded to lower case.
var emailCaptureRE = regexp.MustCompile(`^Email: (.*) — (.*?) ?\[([A-Za-z_]+)\]$`)

// emailTagActions maps the mail filter's triage tag to an inbox action. A tag
// not listed here falls back to the generic heuristics.
var emailTagActions = map[string]string{
	"reply_needed":      InboxActionTask,
	"flag_for_followup": InboxActionTask,
	"read_now":          InboxActionArchive,
	"mark_read":         InboxActionArchive,
	"unsubscribe":       InboxActionArchive,
	"delete":            InboxActionArchive,
}

// emailCaptureTag returns the mail filter's tag when body is a single-line
// email summary capture.
func emailCaptureTag(body []string) (string, bool) {
	if len(body) != 1 {
		return "", false
	}
	m := emailCaptureRE.FindStringSubmatch(body[0])
	if m == nil {
		return "", false
	}
	return strings.ToLower(m[3]), true
}

// repeatKey normalises a capture body for exact-repeat detection: lines
// joined, whitespace collapsed, case folded.
func repeatKey(body []string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.Join(body, "\n")), " "))
}

// CollapseRepeats merges captures whose normalised bodies are identical into
// one item, keeping the first (in input order) as the representative and
// recording the others in Repeats. Order is otherwise preserved. Each item in
// the result stands for 1+len(Repeats) captures; ApplyGroup applies one
// decision to all of them.
func CollapseRepeats(items []InboxItem) []InboxItem {
	out := make([]InboxItem, 0, len(items))
	first := make(map[string]int, len(items))
	for _, it := range items {
		k := repeatKey(it.Body)
		if i, ok := first[k]; ok {
			out[i].Repeats = append(out[i].Repeats, it.Path)
			continue
		}
		first[k] = len(out)
		it.Repeats = nil
		out = append(out, it)
	}
	return out
}
