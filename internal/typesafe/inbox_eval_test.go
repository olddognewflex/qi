//go:build typesafe_eval

// Live evaluation of the inbox fan-out questions (QI-6 spike). It calls the
// real TypeSafe API, so it is behind the typesafe_eval build tag and never
// runs in `go test ./...`:
//
//	TYPESAFE_API_KEY=... go test -tags typesafe_eval -run TestInboxFanoutEval -v ./internal/typesafe
//
// QI_EVAL_SET (default testdata/inbox_eval.jsonl) and QI_EVAL_OPEN_TASKS
// (default testdata/inbox_eval_open_tasks.txt; "none" for no open tasks)
// point it at another labelled set, and QI_EVAL_DESTINATIONS at real
// destinations — keep real captures,
// tasks, and config out of the repo (it is public). QI_EVAL_REPORT
// writes the markdown report to a file as well as the test log.
package typesafe

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

type evalItem struct {
	ID       string `json:"id"`
	Captured string `json:"captured"`
	Text     string `json:"text"`
	Action   string `json:"action"`
	Dest     string `json:"dest"`
	Date     string `json:"date"` // YYYY-MM-DD, "" for no date, "REVIEW" for must-not-resolve
	Role     string `json:"role"`
	DupOf    string `json:"dup_of"` // case-insensitive substring of the duplicated open task
}

// evalDestinations are fictional clients/projects matching the synthetic set.
// QI_EVAL_DESTINATIONS points at a JSON file ([{"key","description"}]) to
// evaluate against a real config instead — keep it out of the repo.
var evalDestinations = []InboxDestination{
	{"Globex", "Globex Corporation (client), the user's employer: the globex GitHub org and its repos, Jira project GLX, the Platform team, Globex meetings, IT and policy."},
	{"portal", "Globex project portal: the globex/portal-platform repo (preview environments, listing and inventory services, CI)."},
	{"graphhub", "Globex project graphhub: the federated GraphQL schema monorepo."},
	{"Initech", "Initech (client): consulting work for Initech that is not one of its projects below."},
	{"tps", "Initech project TPS: the tps-reports app, its hours, invoices, and contracts."},
	{"mapper", "Initech project mapper: the column-mapping tool."},
	{"quant", "Initech project quant: trading strategies and backtests."},
}

func loadDestinations(t *testing.T) []InboxDestination {
	path := os.Getenv("QI_EVAL_DESTINATIONS")
	if path == "" {
		return evalDestinations
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw []struct{ Key, Description string }
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	out := make([]InboxDestination, len(raw))
	for i, r := range raw {
		out[i] = InboxDestination{Key: r.Key, Description: r.Description}
	}
	return out
}

func loadEval(t *testing.T) ([]evalItem, []string) {
	set := envOr("QI_EVAL_SET", "testdata/inbox_eval.jsonl")
	tasksPath := envOr("QI_EVAL_OPEN_TASKS", "testdata/inbox_eval_open_tasks.txt")
	f, err := os.Open(set)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var items []evalItem
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var it evalItem
		if err := json.Unmarshal(sc.Bytes(), &it); err != nil {
			t.Fatalf("%s: %v", set, err)
		}
		items = append(items, it)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("%s: %v", set, err)
	}
	var tasks []string
	if tasksPath == "none" { // explicitly evaluate without open tasks
		return items, nil
	}
	b, err := os.ReadFile(tasksPath)
	if err != nil {
		t.Fatalf("open tasks (set QI_EVAL_OPEN_TASKS=none to skip): %v", err)
	}
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			tasks = append(tasks, l)
		}
	}
	return items, tasks
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

var wordRE = regexp.MustCompile(`[a-z0-9]+`)

var stop = map[string]bool{"the": true, "a": true, "an": true, "to": true, "and": true, "of": true, "for": true, "in": true, "on": true, "with": true, "email": true, "re": true, "your": true, "my": true, "is": true, "it": true, "by": true, "at": true}

func words(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range wordRE.FindAllString(strings.ToLower(s), -1) {
		if !stop[w] && len(w) > 1 {
			out[w] = true
		}
	}
	return out
}

// shortlist is the cheap lexical candidate step: open tasks sharing the most
// words with the capture (Jaccard), top k with any overlap.
func shortlist(text string, tasks []string, k int) []string {
	cw := words(text)
	type sc struct {
		task  string
		score float64
	}
	var scored []sc
	for _, task := range tasks {
		tw := words(task)
		inter := 0
		for w := range cw {
			if tw[w] {
				inter++
			}
		}
		if inter == 0 {
			continue
		}
		scored = append(scored, sc{task, float64(inter) / float64(len(cw)+len(tw)-inter)})
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
	var out []string
	for i := 0; i < len(scored) && i < k; i++ {
		out = append(out, scored[i].task)
	}
	return out
}

type tally struct{ n, correct int }

type dupScore struct {
	p            float64
	isDup, match bool
}

func (t tally) String() string {
	if t.n == 0 {
		return "–"
	}
	return fmt.Sprintf("%d/%d (%.0f%%)", t.correct, t.n, 100*float64(t.correct)/float64(t.n))
}

func (t *tally) add(ok bool) {
	t.n++
	if ok {
		t.correct++
	}
}

// thresholdRow reports, for confidence threshold th, how many items clear it
// (coverage) and how accurate those are.
type scored struct {
	conf float64
	ok   bool
}

func thresholdTable(name string, xs []scored) string {
	var b strings.Builder
	fmt.Fprintf(&b, "| %s threshold | coverage | accuracy above |\n|---|---|---|\n", name)
	for _, th := range []float64{0, 0.5, 0.6, 0.7, 0.8, 0.9} {
		var tl tally
		for _, x := range xs {
			if x.conf >= th {
				tl.add(x.ok)
			}
		}
		fmt.Fprintf(&b, "| %.1f | %d/%d | %s |\n", th, tl.n, len(xs), tl)
	}
	return b.String()
}

func TestInboxFanoutEval(t *testing.T) {
	key := os.Getenv("TYPESAFE_API_KEY")
	if key == "" {
		t.Skip("TYPESAFE_API_KEY unset")
	}
	items, tasks := loadEval(t)
	client := NewClient(os.Getenv("TYPESAFE_URL"), key, os.Getenv("TYPESAFE_MODEL"), &http.Client{Timeout: 90 * time.Second})
	cls := InboxClassifier{Client: client}
	ctx := context.Background()
	const batch = 25

	caps := make([]InboxCapture, len(items))
	for i, it := range items {
		ts, err := time.ParseInLocation("2006-01-02T15:04:05", it.Captured, time.Local)
		if err != nil {
			t.Fatalf("%s captured: %v", it.ID, err)
		}
		caps[i] = InboxCapture{Body: strings.Split(it.Text, "\n"), Captured: ts}
	}

	// Baseline: today's action-only request, for the cost delta.
	var baseUsage Usage
	var baseLatency time.Duration
	baseAction := make([]string, len(items))
	for s := 0; s < len(items); s += batch {
		e := min(s+batch, len(items))
		bodies := make([][]string, 0, e-s)
		for _, c := range caps[s:e] {
			bodies = append(bodies, c.Body)
		}
		t0 := time.Now()
		resp, err := client.SystemOne(ctx, inboxRequest(bodies))
		baseLatency += time.Since(t0)
		if err != nil {
			t.Fatalf("baseline: %v", err)
		}
		baseUsage.InputTokens += resp.Usage.InputTokens
		baseUsage.OutputTokens += resp.Usage.OutputTokens
		for i := s; i < e; i++ {
			baseAction[i] = resp.Answers[inboxQuestionID(i-s)].Choice
		}
	}

	// Fan-out: action + destination + date parts in one request per batch.
	fan := InboxFanout{Destinations: loadDestinations(t), Dates: true, Lean: os.Getenv("QI_EVAL_LEAN") == "1"}
	props := make([]InboxProposal, 0, len(items))
	var fanUsage Usage
	var fanLatency time.Duration
	for s := 0; s < len(items); s += batch {
		e := min(s+batch, len(items))
		t0 := time.Now()
		res, u, err := cls.ProposeInbox(ctx, caps[s:e], fan)
		fanLatency += time.Since(t0)
		if err != nil {
			t.Fatalf("fan-out: %v", err)
		}
		fanUsage.InputTokens += u.InputTokens
		fanUsage.OutputTokens += u.OutputTokens
		props = append(props, res...)
	}

	// Duplicates: lexical shortlist, then one Noul per pair, separate request.
	checks := make([]DupCheck, len(items))
	for i, it := range items {
		checks[i] = DupCheck{Body: caps[i].Body, Candidates: shortlist(it.Text, tasks, 5)}
	}
	dups := make([]DupResult, 0, len(items))
	var dupUsage Usage
	var dupLatency time.Duration
	for s := 0; s < len(items); s += batch {
		e := min(s+batch, len(items))
		t0 := time.Now()
		res, u, err := cls.CheckDuplicates(ctx, checks[s:e])
		dupLatency += time.Since(t0)
		if err != nil {
			t.Fatalf("dups: %v", err)
		}
		dupUsage.InputTokens += u.InputTokens
		dupUsage.OutputTokens += u.OutputTokens
		dups = append(dups, res...)
	}

	// Score.
	var (
		action, actionStable, dest, dateAll, dateNone, dateDated, dateReview, dupAll, dupPos, dupNeg, dupShort tally
		actionS, destS, dateS                                                                                  []scored
		dupTop                                                                                                 []dupScore
		misses                                                                                                 []string
	)
	for i, it := range items {
		p := props[i]
		if p.Err != nil {
			misses = append(misses, fmt.Sprintf("%s: %v", it.ID, p.Err))
			continue
		}
		if p.DateErr != nil { // an incomplete run must not publish date accuracy
			t.Fatalf("%s: %v", it.ID, p.DateErr)
		}
		ok := p.Action == it.Action
		action.add(ok)
		actionS = append(actionS, scored{p.Confidence, ok})
		actionStable.add(p.Action == baseAction[i])
		if !ok {
			misses = append(misses, fmt.Sprintf("action %s: got %s (%.2f) want %s — %q", it.ID, p.Action, p.Confidence, it.Action, trunc(it.Text)))
		}

		if it.Action == "task" {
			ok := p.Destination == it.Dest
			dest.add(ok)
			destS = append(destS, scored{p.DestinationConfidence, ok})
			if !ok {
				misses = append(misses, fmt.Sprintf("dest %s: got %s (%.2f) want %s — %q", it.ID, p.Destination, p.DestinationConfidence, it.Dest, trunc(it.Text)))
			}
		}

		rd := ResolveDate(p.Date, caps[i].Captured)
		var dok bool
		switch it.Date {
		case "":
			dok = rd.Status == DateNone
			dateNone.add(dok)
		case "REVIEW":
			dok = rd.Status != DateOK
			dateReview.add(dok)
		default:
			dok = rd.Status == DateOK && rd.Date.Format("2006-01-02") == it.Date && rd.Role == it.Role
			dateDated.add(dok)
		}
		dateAll.add(dok)
		dateS = append(dateS, scored{rd.Confidence, dok})
		if !dok {
			got := rd.Status
			if rd.Status == DateOK {
				got = rd.Date.Format("2006-01-02") + " " + rd.Role
			}
			misses = append(misses, fmt.Sprintf("date %s: got %s (%.2f; mode=%s anchor=%s wd=%s week=%s month=%s day=%s role=%s) want %q %s — %q",
				it.ID, got, rd.Confidence, p.Date.Mode.Value, p.Date.Anchor.Value, p.Date.Weekday.Value, p.Date.Week.Value, p.Date.Month.Value, p.Date.Day.Value, p.Date.Role.Value, it.Date, it.Role, trunc(it.Text)))
		}

		top, topP := "", -1.0
		for k, pr := range dups[i].Probabilities {
			if pr > topP {
				top, topP = checks[i].Candidates[k], pr
			}
		}
		dupTop = append(dupTop, dupScore{p: topP, isDup: it.DupOf != "", match: it.DupOf != "" && strings.Contains(strings.ToLower(top), strings.ToLower(it.DupOf))})

		// Dup: predicted = highest-probability candidate at >= 0.5.
		best, bestP := "", 0.0
		for k, pr := range dups[i].Probabilities {
			if pr >= 0.5 && pr > bestP {
				best, bestP = checks[i].Candidates[k], pr
			}
		}
		var uok bool
		if it.DupOf == "" {
			uok = best == ""
			dupNeg.add(uok)
		} else {
			uok = best != "" && strings.Contains(strings.ToLower(best), strings.ToLower(it.DupOf))
			dupPos.add(uok)
			inShort := false
			for _, c := range checks[i].Candidates {
				if strings.Contains(strings.ToLower(c), strings.ToLower(it.DupOf)) {
					inShort = true
				}
			}
			dupShort.add(inShort)
		}
		dupAll.add(uok)
		if !uok {
			misses = append(misses, fmt.Sprintf("dup %s: got %q (%.2f) want %q — %q", it.ID, trunc(best), bestP, it.DupOf, trunc(it.Text)))
		}
	}

	pairs := 0
	for _, c := range checks {
		pairs += len(c.Candidates)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Inbox fan-out eval — %s (%d captures, %d open tasks, lean=%v)\n\n", envOr("QI_EVAL_SET", "testdata/inbox_eval.jsonl"), len(items), len(tasks), fan.Lean)
	fmt.Fprintf(&b, "## Accuracy\n\n| judgment | accuracy |\n|---|---|\n")
	fmt.Fprintf(&b, "| action | %s |\n| action unchanged vs action-only request | %s |\n| destination (task-labelled) | %s |\n", action, actionStable, dest)
	fmt.Fprintf(&b, "| date (all) | %s |\n| date: no-date captures → none | %s |\n| date: dated captures → exact date+role | %s |\n| date: must-review captures | %s |\n", dateAll, dateNone, dateDated, dateReview)
	fmt.Fprintf(&b, "| dup (all) | %s |\n| dup: true duplicates found | %s |\n| dup: true duplicate in shortlist | %s |\n| dup: non-dups left alone | %s |\n\n", dupAll, dupPos, dupShort, dupNeg)
	fmt.Fprintf(&b, "%s\n%s\n%s\n", thresholdTable("action", actionS), thresholdTable("destination", destS), thresholdTable("date", dateS))
	fmt.Fprintf(&b, "| dup threshold | flagged | true dups caught | false flags |\n|---|---|---|---|\n")
	for _, th := range []float64{0.5, 0.6, 0.7, 0.8, 0.85, 0.9, 0.95} {
		var flagged, tp, fp, pos int
		for _, d := range dupTop {
			if d.isDup {
				pos++
			}
			if d.p >= th {
				flagged++
				if d.match {
					tp++
				} else {
					fp++
				}
			}
		}
		fmt.Fprintf(&b, "| %.2f | %d | %d/%d | %d |\n", th, flagged, tp, pos, fp)
	}
	fmt.Fprintf(&b, "\n")
	per := func(u Usage) string {
		return fmt.Sprintf("%d in / %d out (%.0f in per capture)", u.InputTokens, u.OutputTokens, float64(u.InputTokens)/float64(len(items)))
	}
	nb := int(math.Ceil(float64(len(items)) / batch))
	fmt.Fprintf(&b, "## Cost and latency (%d batch(es) of ≤%d, sequential)\n\n| request | tokens | wall time | per batch |\n|---|---|---|---|\n", nb, batch)
	fmt.Fprintf(&b, "| action only (today) | %s | %s | %s |\n", per(baseUsage), baseLatency.Round(time.Millisecond), (baseLatency / time.Duration(nb)).Round(time.Millisecond))
	fmt.Fprintf(&b, "| fan-out (action+dest+date) | %s | %s | %s |\n", per(fanUsage), fanLatency.Round(time.Millisecond), (fanLatency / time.Duration(nb)).Round(time.Millisecond))
	fmt.Fprintf(&b, "| duplicates (%d pairs) | %s | %s | %s |\n\n", pairs, per(dupUsage), dupLatency.Round(time.Millisecond), (dupLatency / time.Duration(nb)).Round(time.Millisecond))
	if baseUsage.InputTokens > 0 {
		fmt.Fprintf(&b, "Fan-out input tokens = %.1f× action-only.\n\n", float64(fanUsage.InputTokens)/float64(baseUsage.InputTokens))
	}
	fmt.Fprintf(&b, "## Misses\n\n")
	for _, m := range misses {
		fmt.Fprintf(&b, "- %s\n", m)
	}

	t.Log("\n" + b.String())
	if path := os.Getenv("QI_EVAL_REPORT"); path != "" {
		if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
			t.Error(err)
		}
	}
}

func trunc(s string) string {
	if r := []rune(s); len(r) > 70 {
		return string(r[:70]) + "…"
	}
	return s
}
