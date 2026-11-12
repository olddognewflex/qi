package service

import (
	"context"
	"regexp"
	"sort"
	"strings"
)

// InboxDuplicateShortlist is how many open tasks one capture is checked
// against. The QI-6 spike found every true duplicate in a top-5 lexical
// shortlist; the model only judges candidates it is shown, so this bounds both
// cost and recall.
const InboxDuplicateShortlist = 5

// InboxDupCheck asks whether one capture repeats any of a shortlist of open
// task texts.
type InboxDupCheck struct {
	Body       []string
	Candidates []string
}

// InboxDuplicateChecker judges a batch of duplicate checks in one call. It is
// the seam for the opt-in TypeSafe duplicate check (wired in commands), like
// InboxClassifier.
//
// A non-nil error fails the whole batch. Otherwise the result must be
// index-aligned with checks — one slice of probabilities per check,
// index-aligned with its Candidates, a missing answer being negative — and
// Refine treats a length mismatch as a batch failure.
type InboxDuplicateChecker interface {
	CheckDuplicates(ctx context.Context, checks []InboxDupCheck) ([][]float64, error)
}

// dupWordRE and dupStopWords define the words the lexical shortlist compares.
// "email" and "re" are stop words because the mail filter's summaries start
// with them and would otherwise match every email-derived task.
var (
	dupWordRE    = regexp.MustCompile(`[a-z0-9]+`)
	dupStopWords = map[string]bool{
		"the": true, "a": true, "an": true, "to": true, "and": true, "of": true,
		"for": true, "in": true, "on": true, "with": true, "email": true,
		"re": true, "your": true, "my": true, "is": true, "it": true, "by": true,
		"at": true,
	}
)

// dupWords is the set of lowercase words in s worth comparing: stop words and
// single characters dropped.
func dupWords(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range dupWordRE.FindAllString(strings.ToLower(s), -1) {
		if len(w) > 1 && !dupStopWords[w] {
			out[w] = true
		}
	}
	return out
}

// taskShortlister holds the open tasks' word sets so each capture's shortlist
// does not re-tokenise every task.
type taskShortlister struct {
	tasks []string
	words []map[string]bool
}

// newTaskShortlister indexes the distinct, non-blank open task texts, in
// order (identical texts would only waste a question each).
func newTaskShortlister(tasks []string) taskShortlister {
	var s taskShortlister
	seen := map[string]bool{}
	for _, t := range tasks {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		s.tasks = append(s.tasks, t)
		s.words = append(s.words, dupWords(t))
	}
	return s
}

// shortlist returns up to k open tasks sharing the most words with text by
// Jaccard similarity, best first (ties in task order). Tasks sharing no word
// are never candidates, so a capture with nothing in common gets none.
func (s taskShortlister) shortlist(text string, k int) []string {
	cw := dupWords(text)
	if len(cw) == 0 || k <= 0 {
		return nil
	}
	type scored struct {
		task  string
		score float64
	}
	var hits []scored
	for i, tw := range s.words {
		inter := 0
		for w := range cw {
			if tw[w] {
				inter++
			}
		}
		if inter == 0 {
			continue
		}
		hits = append(hits, scored{s.tasks[i], float64(inter) / float64(len(cw)+len(tw)-inter)})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	out := make([]string, 0, min(k, len(hits)))
	for i := 0; i < len(hits) && i < k; i++ {
		out = append(out, hits[i].task)
	}
	return out
}

// ShortlistOpenTasks returns up to k of tasks sharing the most words with
// text (Jaccard over lowercase words, stop words dropped), best first. It is
// the cheap candidate step before the duplicate check: the FTS index is
// file-granular and cannot rank individual task lines.
func ShortlistOpenTasks(text string, tasks []string, k int) []string {
	return newTaskShortlister(tasks).shortlist(text, k)
}

// applyDuplicate flags it as a possible duplicate of the most probable
// candidate when that probability reaches floor, and reports whether it did.
// Negative (missing) probabilities never count.
func applyDuplicate(it *InboxItem, candidates []string, probs []float64, floor float64) bool {
	best := -1
	for k, p := range probs {
		if k < len(candidates) && p >= 0 && (best < 0 || p > probs[best]) {
			best = k
		}
	}
	if best < 0 || probs[best] < floor {
		return false
	}
	it.DuplicateOf = candidates[best]
	it.DuplicateProbability = probs[best]
	return true
}

// answeredAny reports whether probs holds at least one real answer.
func answeredAny(probs []float64) bool {
	for _, p := range probs {
		if p >= 0 {
			return true
		}
	}
	return false
}
