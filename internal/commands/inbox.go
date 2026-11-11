package commands

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"qi/internal/config"
	"qi/internal/domain"
	"qi/internal/service"
	"qi/internal/tui"
	"qi/internal/typesafe"
)

// Classification timeouts. Each TypeSafe request carries a batch of
// service.InboxClassifyBatchSize captures, so its HTTP timeout is per batch;
// the overall budget scales with how many rounds of
// service.InboxClassifyConcurrency concurrent batches the run needs, capped so
// a huge inbox cannot stall triage. On expiry the unfinished captures keep
// their heuristic proposals.
const (
	inboxBatchTimeout    = 30 * time.Second
	inboxClassifyMaxWait = 3 * time.Minute
)

// inboxClassifyBudget is the overall classification timeout for batches
// requests: ceil(batches/concurrency) rounds of inboxBatchTimeout, capped at
// inboxClassifyMaxWait.
func inboxClassifyBudget(batches int) time.Duration {
	rounds := (batches + service.InboxClassifyConcurrency - 1) / service.InboxClassifyConcurrency
	if rounds < 1 {
		rounds = 1
	}
	return min(time.Duration(rounds)*inboxBatchTimeout, inboxClassifyMaxWait)
}

// buildInboxClassifier turns [typesafe] config plus the resolved API key into
// the service-layer classifier. A package var so tests can inject a fake.
var buildInboxClassifier = func(cfg config.Config, apiKey string) service.InboxClassifier {
	client := typesafe.NewClient(cfg.TypeSafe.URL, apiKey, cfg.TypeSafe.Model, &http.Client{Timeout: inboxBatchTimeout})
	return typesafeInboxClassifier{typesafe.InboxClassifier{Client: client}}
}

// typesafeInboxClassifier adapts typesafe's plain-value verdicts to the
// service interface, keeping typesafe free of a service import.
type typesafeInboxClassifier struct {
	inner typesafe.InboxClassifier
}

// ClassifyInbox asks only the action question when dests is empty (the
// request is exactly the pre-destination one); otherwise it adds QI-6's lean
// destination fan-out (descriptions in shared state, one routing Choice per
// capture). Dates stay off.
func (c typesafeInboxClassifier) ClassifyInbox(ctx context.Context, bodies [][]string, dests []service.InboxDestination) ([]service.InboxVerdict, error) {
	if len(dests) == 0 {
		results, err := c.inner.ClassifyInbox(ctx, bodies)
		if err != nil {
			return nil, err
		}
		out := make([]service.InboxVerdict, len(results))
		for i, r := range results {
			out[i] = service.InboxVerdict{
				InboxClassification: service.InboxClassification{Action: r.Action, Confidence: r.Confidence, Probabilities: r.Probabilities},
				Err:                 r.Err,
			}
		}
		return out, nil
	}
	caps := make([]typesafe.InboxCapture, len(bodies))
	for i, b := range bodies {
		caps[i] = typesafe.InboxCapture{Body: b}
	}
	fan := typesafe.InboxFanout{Destinations: make([]typesafe.InboxDestination, len(dests)), Lean: true}
	for i, d := range dests {
		fan.Destinations[i] = typesafe.InboxDestination{Key: d.Key, Description: d.Description}
	}
	proposals, _, err := c.inner.ProposeInbox(ctx, caps, fan)
	if err != nil {
		return nil, err
	}
	out := make([]service.InboxVerdict, len(proposals))
	for i, p := range proposals {
		out[i] = service.InboxVerdict{
			InboxClassification:   service.InboxClassification{Action: p.Action, Confidence: p.Confidence, Probabilities: p.Probabilities},
			Destination:           p.Destination,
			DestinationConfidence: p.DestinationConfidence,
			Err:                   p.Err,
		}
	}
	return out, nil
}

// inboxDestinations lists where the classifier may route a task: every
// configured (non-synthetic) project, keyed by its #tag, then every client,
// keyed by its name — the same tags `qi task add --project/--client` write.
// Descriptions come from config, with a bare fallback. A key the classifier
// cannot offer (not letters/digits/-/_, all digits, "none") or one colliding
// with an earlier key (case-insensitively) is skipped with one warning on
// errOut.
func inboxDestinations(cfg config.Config, errOut io.Writer) []service.InboxDestination {
	var (
		out     []service.InboxDestination
		skipped []string
		seen    = map[string]bool{}
	)
	add := func(d service.InboxDestination) {
		k := strings.ToLower(d.Key)
		if !typesafe.ValidDestinationKey(d.Key) || domain.IsNumericTag(d.Key) || seen[k] {
			skipped = append(skipped, d.Key)
			return
		}
		seen[k] = true
		out = append(out, d)
	}
	for _, p := range cfg.Projects {
		if p.IsSynthetic() {
			continue
		}
		desc := p.Description
		if desc == "" {
			desc = fmt.Sprintf("The %s project for client %s.", p.Project, p.Client)
		}
		add(service.InboxDestination{Key: p.Project, Description: desc})
	}
	for _, cl := range cfg.Clients {
		desc := cl.Description
		if desc == "" {
			desc = fmt.Sprintf("Client %s, when no one of its projects fits.", cl.Name)
		}
		add(service.InboxDestination{Key: cl.Name, Description: desc, Client: true})
	}
	if len(skipped) > 0 {
		fmt.Fprintf(errOut, "inbox: typesafe: not offering destination(s) %s (keys must be unique letters, digits, - or _)\n", strings.Join(skipped, ", "))
	}
	return out
}

// destinationTag is the destination proposed for it, "" when none.
func destinationTag(it service.InboxItem) string {
	return service.TaskRouteTag(it.Project, it.Client)
}

// destinationSuffix renders a task item's destination for --dry-run output.
func destinationSuffix(it service.InboxItem) string {
	if t := destinationTag(it); t != "" && it.Action == service.InboxActionTask {
		return " → #" + t
	}
	return ""
}

// refineInbox applies the opt-in TypeSafe classifier to items when selected,
// sending at most limit eligible captures (0 = no limit). It never fails
// triage: a missing key or a classifier error warns on errOut and the
// heuristic proposals stand. Progress and the summary go to errOut too, so
// stdout stays clean for --dry-run.
func refineInbox(ctx context.Context, cfg config.Config, inbox service.InboxService, items []service.InboxItem, classifier string, limit int, errOut io.Writer) []service.InboxItem {
	if classifier != config.InboxClassifierTypeSafe {
		return items
	}
	keyEnv := cfg.TypeSafe.APIKeyEnv
	if keyEnv == "" {
		keyEnv = config.DefaultTypeSafeAPIKeyEnv
	}
	apiKey := os.Getenv(keyEnv)
	if apiKey == "" {
		fmt.Fprintf(errOut, "inbox: %s unset; using heuristic proposals\n", keyEnv)
		return items
	}
	opts := service.InboxRefineOptions{MinConfidence: cfg.Inbox.MinConfidence, Limit: limit}
	plan := inbox.PlanRefine(items, opts)
	if plan.Eligible == 0 {
		return items
	}
	opts.Destinations = inboxDestinations(cfg, errOut)
	opts.MinDestinationConfidence = cfg.Inbox.MinDestinationConfidence
	if plan.Selected > 0 {
		fmt.Fprintf(errOut, "inbox: typesafe: classifying %d capture(s) in %d request(s)…\n", plan.Selected, plan.Batches)
	}
	if plan.Skipped > 0 {
		fmt.Fprintf(errOut, "inbox: typesafe: %d more capture(s) over classify_limit %d keep heuristic proposals\n", plan.Skipped, limit)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, inboxClassifyBudget(plan.Batches))
	defer cancel()
	refined, stats, err := inbox.Refine(ctx, items, buildInboxClassifier(cfg, apiKey), opts)
	if err != nil {
		fmt.Fprintf(errOut, "inbox: typesafe: %v\n", err)
	}
	if plan.Selected > 0 {
		routed := ""
		if len(opts.Destinations) > 0 {
			routed = fmt.Sprintf(", routed %d", stats.Routed)
		}
		fmt.Fprintf(errOut, "inbox: typesafe: refined %d, unsure %d, failed %d%s\n", stats.Refined, stats.Unsure, stats.Failed, routed)
	}
	return refined
}

// triageInbox runs the interactive triage; a package var so tests can drive
// the apply path without a terminal.
var triageInbox = tui.TriageInbox

// repeatPrefix marks an item standing for several identical captures.
func repeatPrefix(it service.InboxItem) string {
	if len(it.Repeats) == 0 {
		return ""
	}
	return fmt.Sprintf("×%d ", 1+len(it.Repeats))
}

// captureCount describes distinct items vs the captures they stand for.
func captureCount(items, captures int) string {
	if items == captures {
		return fmt.Sprintf("%d capture(s)", captures)
	}
	return fmt.Sprintf("%d distinct of %d capture(s)", items, captures)
}

func newInboxCommand(cfg config.Config) *cobra.Command {
	inbox := service.InboxService{
		InboxDir:   cfg.InboxPath,
		ArchiveDir: filepath.Join(cfg.InboxPath, "archive"),
		Tasks:      service.NewTaskService(cfg.TaskFilePath),
		Notes:      service.NewNoteService(cfg.NotesPath),
		IsClient:   func(name string) bool { _, ok := cfg.ClientByName(name); return ok },
	}

	var (
		dryRun        bool
		classifier    string
		classifyLimit int
	)

	cmd := &cobra.Command{
		Use:   "inbox",
		Short: "Triage 00-inbox captures interactively",
		Long: "Triage each capture in 00-inbox: turn it into a task or note, archive\n" +
			"it, or delete it. Captures open in a paged list showing each one's\n" +
			"proposed action (dimmed with a ? until you decide) and its source (rule,\n" +
			"ts 0.91, ts? unsure, lim over classify_limit). --dry-run prints the\n" +
			"proposals without writing.\n\n" +
			"List keys: j/k or arrows move, pgup/pgdn (ctrl+u/ctrl+d) page, g/G top/\n" +
			"bottom; t task, n note, a archive, d delete, s skip, enter accept the\n" +
			"proposal (each moves down); A accepts every undecided proposal; x clears\n" +
			"(or restores) a row's proposed destination. space/tab opens the full\n" +
			"card (←/→ move, same decision keys, tab/esc back). w finishes and\n" +
			"applies; undecided captures are skipped. q quits without applying\n" +
			"anything.\n\n" +
			"Identical captures are collapsed into one row marked ×N; the decision\n" +
			"applies to all of them (one task or note, the rest archived — or all\n" +
			"deleted). One-line email summaries (\"Email: <subject> — <sender>\n" +
			"[tag]\") are proposed from their mail-filter tag: reply_needed and\n" +
			"flag_for_followup → task; read_now, mark_read, unsubscribe, delete →\n" +
			"archive (source \"email\").\n\n" +
			"Proposals come from deterministic heuristics by default. Opt in to the\n" +
			"TypeSafe classifier with --classifier typesafe (or [inbox] classifier =\n" +
			"\"typesafe\") to refine the non-obvious ones: this SENDS CAPTURE TEXT to\n" +
			"api.typesafe.ai, using the key in $TYPESAFE_API_KEY ([typesafe]\n" +
			"api_key_env). Empty captures and explicit task markers never leave the\n" +
			"machine. Below [inbox] min_confidence (default 0.5), or on any API error,\n" +
			"the heuristic proposal stands. Captures go 25 per request, and at most\n" +
			"[inbox] classify_limit (default 100; --classify-limit, 0 = no limit) are\n" +
			"sent per run; the rest keep heuristic proposals. Nothing is written\n" +
			"without your choice.\n\n" +
			"With [[client]]s configured, the classifier also proposes a destination\n" +
			"for tasks: a [[client.project]] tag or a client name, judged from each\n" +
			"one's optional `description` (names and descriptions are sent too; the\n" +
			"extra question can tip near-tie actions). It shows as → #tag only when\n" +
			"the capture ends up a task and the destination confidence is at least\n" +
			"[inbox] min_destination_confidence (default 0.7); otherwise nothing is\n" +
			"proposed. Accepting routes the task like qi task add --project/--client\n" +
			"(tagged into 10-tasks/<tag>.md; sync moves it to the configured\n" +
			"task_file, if any); a note, archive or delete drops it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()

			selected := cfg.Inbox.Classifier
			if classifier != "" {
				selected = classifier
			}
			if selected != "" && !config.ValidInboxClassifier(selected) {
				return fmt.Errorf("unknown --classifier %q (want %s or %s)", selected, config.InboxClassifierHeuristic, config.InboxClassifierTypeSafe)
			}

			limit := cfg.Inbox.ClassifyLimit
			if cmd.Flags().Changed("classify-limit") {
				if classifyLimit < 0 {
					return fmt.Errorf("--classify-limit %d must be >= 0 (0 = no limit)", classifyLimit)
				}
				limit = classifyLimit
			}

			items, err := inbox.List()
			if err != nil {
				return err
			}
			if len(items) == 0 {
				fmt.Fprintln(out, "inbox empty — nothing to triage.")
				return nil
			}
			// Collapse exact repeats first so a repeated capture is triaged
			// (and, opted in, sent to the classifier) once.
			items = service.CollapseRepeats(items)
			items = refineInbox(cmd.Context(), cfg, inbox, items, selected, limit, cmd.ErrOrStderr())

			if dryRun {
				total := 0
				for _, it := range items {
					fmt.Fprintf(out, "%-7s %s%s%s  (%s)\n", it.Action, repeatPrefix(it), it.Summary, destinationSuffix(it), it.Reason)
					total += 1 + len(it.Repeats)
				}
				fmt.Fprintf(out, "\n%s; run without --dry-run to triage.\n", captureCount(len(items), total))
				return nil
			}

			cards := make([]tui.InboxCard, len(items))
			for i, it := range items {
				cards[i] = tui.InboxCard{Summary: it.Summary, Body: it.Body, Proposed: it.Action, Reason: it.Reason, Count: 1 + len(it.Repeats), Destination: destinationTag(it)}
			}

			decisions, err := triageInbox(cards)
			if err != nil {
				return err
			}
			if decisions == nil {
				fmt.Fprintln(out, "aborted — nothing applied.")
				return nil
			}

			var applied, skipped, routed int
			for i, d := range decisions {
				action := d.Action
				if action == service.InboxActionTask ||
					action == service.InboxActionNote ||
					action == service.InboxActionArchive ||
					action == service.InboxActionDelete {
					// The TUI hands back the destination only for a task
					// that kept it; anything else applies unrouted.
					it := items[i]
					if d.Destination == "" {
						it.Project, it.Client = "", ""
					}
					res, err := inbox.ApplyGroup(it, action)
					applied += len(res)
					if err != nil {
						// No task or note was created for this row (creation is
						// ApplyGroup's last step); say what was already moved.
						fmt.Fprintf(out, "\n%d capture(s) applied before the error; %s left for the next run.\n", applied, it.Summary)
						return fmt.Errorf("apply %s: %w", it.Summary, err)
					}
					dest := ""
					if d.Destination != "" {
						routed++
						dest = " → #" + d.Destination
					}
					fmt.Fprintf(out, "%-7s %s%s%s\n", action, repeatPrefix(it), it.Summary, dest)
					continue
				}
				skipped += 1 + len(items[i].Repeats)
			}
			if routed > 0 {
				fmt.Fprintf(out, "\n%d applied, %d skipped, routed %d.\n", applied, skipped, routed)
				return nil
			}
			fmt.Fprintf(out, "\n%d applied, %d skipped.\n", applied, skipped)
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print proposed actions without writing")
	cmd.Flags().StringVar(&classifier, "classifier", "", "proposal source: heuristic|typesafe (default [inbox] classifier; typesafe sends capture text to api.typesafe.ai)")
	cmd.Flags().IntVar(&classifyLimit, "classify-limit", 0, "max captures sent to the classifier this run (default [inbox] classify_limit; 0 = no limit)")
	return cmd
}
