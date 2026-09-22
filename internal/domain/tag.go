package domain

import "regexp"

// TagBodyPattern is the body of an Obsidian-compatible #tag (without the
// leading "#"): letters, digits, "_", "-" and "/", with AT LEAST ONE
// non-digit character. Obsidian does not treat an all-digit "#14" or "#2026"
// as a tag, so qi must not either — otherwise text like "PR #14" silently
// becomes project "14" and the task is routed into 10-tasks/14.md.
//
// Go's regexp has no lookahead, so "not all digits" is spelled as
// digits-or-tag-chars, one required non-digit, then digits-or-tag-chars.
// It is the single source of truth for every tag matcher in qi (vault task
// and note parsing, the ## Schedule parser, project validation).
const TagBodyPattern = `[A-Za-z0-9_\-/]*[A-Za-z_\-/][A-Za-z0-9_\-/]*`

var tagNameRe = regexp.MustCompile(`^` + TagBodyPattern + `$`)

// IsTag reports whether name (without the leading "#") is a valid
// Obsidian-compatible tag body: tag charset only, and not all digits.
func IsTag(name string) bool {
	return tagNameRe.MatchString(name)
}

// IsNumericTag reports whether name (without the leading "#") is non-empty
// and made only of ASCII digits — the "#14" / "#2026" case Obsidian refuses
// to treat as a tag. Parsers with a looser token rule than TagBodyPattern
// (the ## Schedule parser takes any "#word") use it to apply the same
// numeric exclusion without narrowing what they otherwise accept.
func IsNumericTag(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
