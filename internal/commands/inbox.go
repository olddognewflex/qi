package commands

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"qi/internal/config"
	"qi/internal/service"
	"qi/internal/tui"
)

func newInboxCommand(cfg config.Config) *cobra.Command {
	inbox := service.InboxService{
		InboxDir:   cfg.InboxPath,
		ArchiveDir: filepath.Join(cfg.InboxPath, "archive"),
		Tasks: service.TaskService{
			TaskFilePath: cfg.TaskFilePath,
			TasksDir:     filepath.Dir(cfg.TaskFilePath),
		},
		Notes: service.NoteService{NotesDir: cfg.NotesPath},
	}

	var dryRun bool

	cmd := &cobra.Command{
		Use:   "inbox",
		Short: "Triage 00-inbox captures interactively",
		Long: "Walk each capture in 00-inbox and turn it into a task or note, archive\n" +
			"it, or delete it. Each capture is shown with a proposed action you can\n" +
			"accept or override. --dry-run prints the proposals without writing.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()

			items, err := inbox.List()
			if err != nil {
				return err
			}
			if len(items) == 0 {
				fmt.Fprintln(out, "inbox empty — nothing to triage.")
				return nil
			}

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
	return cmd
}
