package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	"qi/internal/config"
	"qi/internal/service"
)

func newCaptureCommand(cfg config.Config) *cobra.Command {
	svc := service.CaptureService{InboxPath: cfg.InboxPath}

	captureCmd := &cobra.Command{
		Use:     "capture <text>",
		Aliases: []string{"c"},
		Short:   "Capture a thought to inbox (fast, no blocking)",
		Example: "  qi capture \"idea: batch the drain timer\"\n" +
			"  qi c \"call the dentist\"",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := svc.Capture(args[0]); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Captured.")
			return nil
		},
	}

	return captureCmd
}
