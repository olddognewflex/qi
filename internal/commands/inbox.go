package commands

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"qi/internal/config"
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

func (c typesafeInboxClassifier) ClassifyInbox(ctx context.Context, bodies [][]string) ([]service.InboxVerdict, error) {
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
		fmt.Fprintf(errOut, "inbox: typesafe: refined %d, unsure %d, failed %d\n", stats.Refined, stats.Unsure, stats.Failed)
	}
	return refined
}

func newInboxCommand(cfg config.Config) *cobra.Command {
	inbox := service.InboxService{
		InboxDir:   cfg.InboxPath,
		ArchiveDir: filepath.Join(cfg.InboxPath, "archive"),
		Tasks:      service.NewTaskService(cfg.TaskFilePath),
		Notes:      service.NewNoteService(cfg.NotesPath),
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
			"proposal (each moves down); A accepts every undecided proposal. space/tab\n" +
			"opens the full card (←/→ move, same decision keys, tab/esc back). w\n" +
			"finishes and applies; undecided captures are skipped. q quits without\n" +
			"applying anything.\n\n" +
			"Proposals come from deterministic heuristics by default. Opt in to the\n" +
			"TypeSafe classifier with --classifier typesafe (or [inbox] classifier =\n" +
			"\"typesafe\") to refine the non-obvious ones: this SENDS CAPTURE TEXT to\n" +
			"api.typesafe.ai, using the key in $TYPESAFE_API_KEY ([typesafe]\n" +
			"api_key_env). Empty captures and explicit task markers never leave the\n" +
			"machine. Below [inbox] min_confidence (default 0.5), or on any API error,\n" +
			"the heuristic proposal stands. Captures go 25 per request, and at most\n" +
			"[inbox] classify_limit (default 100; --classify-limit, 0 = no limit) are\n" +
			"sent per run; the rest keep heuristic proposals. Nothing is written\n" +
			"without your choice.",
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
			items = refineInbox(cmd.Context(), cfg, inbox, items, selected, limit, cmd.ErrOrStderr())

			if dryRun {
				for _, it := range items {
					fmt.Fprintf(out, "%-7s %s  (%s)\n", it.Action, it.Summary, it.Reason)
				}
				fmt.Fprintf(out, "\n%d capture(s); run without --dry-run to triage.\n", len(items))
				return nil
			}

			cards := make([]tui.InboxCard, len(items))
			for i, it := range items {
				cards[i] = tui.InboxCard{Summary: it.Summary, Body: it.Body, Proposed: it.Action, Reason: it.Reason}
			}

			actions, err := tui.TriageInbox(cards)
			if err != nil {
				return err
			}
			if actions == nil {
				fmt.Fprintln(out, "aborted — nothing applied.")
				return nil
			}

			var applied, skipped int
			for i, action := range actions {
				if action == service.InboxActionTask ||
					action == service.InboxActionNote ||
					action == service.InboxActionArchive ||
					action == service.InboxActionDelete {
					res, err := inbox.Apply(service.InboxApplyInput{Path: items[i].Path, Action: action})
					if err != nil {
						return fmt.Errorf("apply %s: %w", items[i].Summary, err)
					}
					fmt.Fprintf(out, "%-7s %s\n", res.Action, items[i].Summary)
					applied++
					continue
				}
				skipped++
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
