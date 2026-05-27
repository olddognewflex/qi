package commands

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"
	"qi/internal/config"
	"qi/internal/index"
	"qi/internal/sync"
)

func newSyncCommand(cfg config.Config) *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Reconcile tasks between the main vault and project vaults",
		Long: "Run a global bidirectional reconcile between the main vault's per-project task " +
			"files and each configured project_vault projection file. Use --dry-run to preview " +
			"the plan without writing anything.",
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if len(cfg.ProjectVaults) == 0 {
				fmt.Fprintln(out, "No project_vault entries configured; nothing to sync.")
				return nil
			}

			idx, err := index.Open()
			if err != nil {
				return fmt.Errorf("open index: %w", err)
			}
			defer idx.Close()

			rep, err := sync.Reconcile(cfg, idx, dryRun)
			if err != nil {
				return err
			}

			paths := make([]string, 0, len(rep.Files))
			for p := range rep.Files {
				paths = append(paths, p)
			}
			sort.Strings(paths)

			if dryRun {
				fmt.Fprintln(out, "Dry run — no files written, base not committed.")
			}
			if len(paths) == 0 {
				fmt.Fprintln(out, "Already in sync.")
			}
			for _, p := range paths {
				fc := rep.Files[p]
				fmt.Fprintf(out, "%s: +%d -%d (%d tasks)\n", p, fc.Added, fc.Removed, fc.Total)
			}
			for _, c := range rep.Conflicts {
				fmt.Fprintf(out, "conflict: %s (project %s) kept both, tagged #%s\n", c.ID, c.Project, sync.SyncConflictTag)
			}
			for _, s := range rep.Skipped {
				fmt.Fprintf(out, "skipped (kept racing with external writes): %s\n", s)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the plan without writing or committing base")
	return cmd
}
