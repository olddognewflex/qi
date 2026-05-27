package sync

import (
	"sort"

	"qi/internal/domain"
	"qi/internal/vault"
)

// renderFile produces the byte content of a managed task file from the desired
// task set, plus the count of task lines written. qi owns these files outright,
// so the file is the canonical task lines and nothing else.
//
// Ordering is DETERMINISTIC and IDEMPOTENT: tasks whose id appeared in the prior
// on-disk file keep their prior relative order; brand-new ids are appended after
// them sorted by id. A second reconcile with no external change therefore
// re-derives the identical task set in the identical order and produces a
// byte-identical file (and an empty plan delta), which is what makes repeated
// runs no-ops.
func renderFile(prior, desired []domain.Task) (content []byte, total int) {
	// prior order index: id -> position in the prior file.
	priorPos := make(map[string]int, len(prior))
	for i, t := range prior {
		if t.ID != "" {
			if _, seen := priorPos[t.ID]; !seen {
				priorPos[t.ID] = i
			}
		}
	}

	// Deduplicate desired by id, but a conflict appends two entries for the SAME
	// id (the original and the #sync-conflict copy). Keep both: dedupe only on
	// the canonical line so identical duplicates collapse while a tagged copy
	// (different canonical line) survives.
	seenLine := make(map[string]struct{}, len(desired))
	ordered := make([]domain.Task, 0, len(desired))
	for _, t := range desired {
		line := canonicalize(t)
		if line == "" {
			continue
		}
		if _, dup := seenLine[line]; dup {
			continue
		}
		seenLine[line] = struct{}{}
		ordered = append(ordered, t)
	}

	sort.SliceStable(ordered, func(i, j int) bool {
		a, aKnown := priorPos[ordered[i].ID]
		b, bKnown := priorPos[ordered[j].ID]
		switch {
		case aKnown && bKnown:
			return a < b
		case aKnown != bKnown:
			return aKnown // known-prior ids sort before brand-new ids
		default:
			// both new: sort by canonical line for a stable, content-derived order.
			// (id alone is not enough when a conflict places two lines per id.)
			return canonicalize(ordered[i]) < canonicalize(ordered[j])
		}
	})

	buf := make([]byte, 0, 64*len(ordered))
	for _, t := range ordered {
		line := canonicalize(t)
		if line == "" {
			continue
		}
		buf = append(buf, line...)
		buf = append(buf, '\n')
		total++
	}
	return buf, total
}

// lineDelta reports how many canonical task lines were added and removed between
// the prior on-disk set and the desired set (for human-readable reporting).
func lineDelta(prior, desired []domain.Task) (added, removed int) {
	priorLines := map[string]int{}
	for _, t := range prior {
		priorLines[canonicalize(t)]++
	}
	desiredLines := map[string]int{}
	for _, t := range desired {
		line := canonicalize(t)
		if line == "" {
			continue
		}
		desiredLines[line]++
	}
	for line, dn := range desiredLines {
		if pn := priorLines[line]; dn > pn {
			added += dn - pn
		}
	}
	for line, pn := range priorLines {
		if dn := desiredLines[line]; pn > dn {
			removed += pn - dn
		}
	}
	return added, removed
}

// needsWrite reports whether the file at path differs from content. A
// byte-identical file is skipped so reconcile never churns mtimes needlessly,
// keeping idempotency cheap and TOCTOU retries rare.
func needsWrite(path string, content []byte) bool {
	existing, err := vault.ReadFileBytes(path)
	if err != nil {
		// Treat unreadable/absent as "needs write" so a brand-new non-empty file
		// is created; an empty content for an absent file is a no-op below.
		return len(content) > 0
	}
	if len(existing) != len(content) {
		return true
	}
	for i := range existing {
		if existing[i] != content[i] {
			return true
		}
	}
	return false
}
