package commands

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"qi/internal/config"
	"qi/internal/domain"
	"qi/internal/index"
)

// validSearchKinds are the kind labels accepted by --kind, mapped to the domain
// consts the index classifier emits.
var validSearchKinds = map[string]bool{
	domain.SearchKindNote:  true,
	domain.SearchKindTask:  true,
	domain.SearchKindDaily: true,
	domain.SearchKindInbox: true,
	domain.SearchKindOther: true,
}

func newSearchCommand(cfg config.Config) *cobra.Command {
	var kinds []string
	var limit int

	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search the whole vault (notes, tasks, dailies, inbox) via the FTS index",
		Long: "Full-text search across all vault markdown, labelling each hit by kind\n" +
			"(note / task / daily / inbox / other) derived from the vault subdir.\n" +
			"Filter with --kind and cap results with --limit.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, k := range kinds {
				if !validSearchKinds[k] {
					return fmt.Errorf("unknown kind %q; valid kinds: note, task, daily, inbox, other", k)
				}
			}

			idx, err := index.Open()
			if err != nil {
				return fmt.Errorf("open index: %w", err)
			}
			defer idx.Close()

			results, err := idx.SearchWith(args[0], index.SearchOptions{Kinds: kinds, Limit: limit})
			if err != nil {
				return fmt.Errorf("search: %w", err)
			}
			if len(results) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No matches.")
				return nil
			}
			for _, r := range results {
				rel, err := filepath.Rel(cfg.VaultPath, r.Note.Path)
				if err != nil {
					rel = r.Note.Path
				}
				fmt.Fprintf(cmd.OutOrStdout(), "[%s]\t%s\n  %s\n\n", r.Kind, rel, strings.TrimSpace(r.Match))
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVarP(&kinds, "kind", "k", nil, "limit to kinds (comma-separated): note,task,daily,inbox,other")
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "max results")
	return cmd
}
