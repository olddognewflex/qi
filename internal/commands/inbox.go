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

// inboxClassifyTimeout bounds the whole opt-in classification pass; on expiry
// the unfinished captures keep their heuristic proposals.
const inboxClassifyTimeout = 20 * time.Second

// buildInboxClassifier turns [typesafe] config plus the resolved API key into
// the service-layer classifier. A package var so tests can inject a fake.
var buildInboxClassifier = func(cfg config.Config, apiKey string) service.InboxClassifier {
	client := typesafe.NewClient(cfg.TypeSafe.URL, apiKey, cfg.TypeSafe.Model, &http.Client{Timeout: 15 * time.Second})
	return typesafeInboxClassifier{typesafe.InboxClassifier{Client: client}}
}

// typesafeInboxClassifier adapts typesafe's plain-value verdict to the
// service interface, keeping typesafe free of a service import.
type typesafeInboxClassifier struct {
	inner typesafe.InboxClassifier
}

func (c typesafeInboxClassifier) ClassifyInbox(ctx context.Context, body []string) (service.InboxClassification, error) {
	cls, err := c.inner.ClassifyInbox(ctx, body)
	if err != nil {
		return service.InboxClassification{}, err
	}
	return service.InboxClassification{Action: cls.Action, Confidence: cls.Confidence, Probabilities: cls.Probabilities}, nil
}

// refineInbox applies the opt-in TypeSafe classifier to items when selected.
// It never fails triage: a missing key or a classifier error warns on errOut
// and the heuristic proposals stand.
func refineInbox(ctx context.Context, cfg config.Config, inbox service.InboxService, items []service.InboxItem, classifier string, errOut io.Writer) []service.InboxItem {
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
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, inboxClassifyTimeout)
	defer cancel()
	refined, err := inbox.Refine(ctx, items, buildInboxClassifier(cfg, apiKey), cfg.Inbox.MinConfidence)
	if err != nil {
		fmt.Fprintf(errOut, "inbox: typesafe: %v\n", err)
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
		dryRun     bool
		classifier string
	)

	cmd := &cobra.Command{
		Use:   "inbox",
		Short: "Triage 00-inbox captures interactively",
		Long: "Walk each capture in 00-inbox and turn it into a task or note, archive\n" +
			"it, or delete it. Each capture is shown with a proposed action you can\n" +
			"accept or override. --dry-run prints the proposals without writing.\n\n" +
			"Proposals come from deterministic heuristics by default. Opt in to the\n" +
			"TypeSafe classifier with --classifier typesafe (or [inbox] classifier =\n" +
			"\"typesafe\") to refine the non-obvious ones: this SENDS CAPTURE TEXT to\n" +
			"api.typesafe.ai, using the key in $TYPESAFE_API_KEY ([typesafe]\n" +
			"api_key_env). Empty captures and explicit task markers never leave the\n" +
			"machine. Below [inbox] min_confidence (default 0.5), or on any API error,\n" +
			"the heuristic proposal stands. Nothing is written without your choice.",
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

			items, err := inbox.List()
			if err != nil {
				return err
			}
			if len(items) == 0 {
				fmt.Fprintln(out, "inbox empty — nothing to triage.")
				return nil
			}
			items = refineInbox(cmd.Context(), cfg, inbox, items, selected, cmd.ErrOrStderr())

			if dryRun {
				for _, it := range items {
					fmt.Fprintf(out, "%-7s %s  (%s)\n", it.Action, it.Summary, it.Reason)
				}
				fmt.Fprintf(out, "\n%d capture(s); run without --dry-run to triage.\n", len(items))
				return nil
			}

			cards := make([]tui.InboxCard, len(items))
			for i, it := range items {
				cards[i] = tui.InboxCard{Summary: it.Summary, Body: it.Body, Proposed: it.Action}
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
	return cmd
}
