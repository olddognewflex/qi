package commands

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"qi/internal/config"
	"qi/internal/index"
)

func newIndexCommand(cfg config.Config) *cobra.Command {
	idxCmd := &cobra.Command{
		Use:   "index",
		Short: "Manage the local search index",
	}

	rebuildCmd := &cobra.Command{
		Use:   "rebuild",
		Short: "Rebuild FTS index from vault markdown",
		RunE: func(cmd *cobra.Command, args []string) error {
			idx, err := index.Open()
			if err != nil {
				return fmt.Errorf("open index: %w", err)
			}
			defer idx.Close()

			start := time.Now()
			if err := idx.Rebuild(cfg.VaultPath); err != nil {
				return fmt.Errorf("rebuild: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Indexed in %s\n", time.Since(start).Round(time.Millisecond))
			return nil
		},
	}

	idxCmd.AddCommand(rebuildCmd)
	return idxCmd
}
