package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	"qi/internal/config"
	"qi/internal/remotequeue"
)

func newRemoteStatusCommand(cfg config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remote-status",
		Short: "Show pending and deadletter counts in the cloud queue",
		Long: "Query the cloud queue for how many tasks are waiting to be drained\n" +
			"(pending) and how many failed validation and were set aside (deadletter).\n" +
			"Read-only — does not pull, ack, or delete. A no-op when [remote_queue]\n" +
			"is disabled.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if !cfg.RemoteQueue.Enabled {
				fmt.Fprintln(out, "remote queue disabled (set [remote_queue].enabled or QI_QUEUE_ENABLED); nothing to report.")
				return nil
			}
			if cfg.RemoteQueue.URL == "" {
				return fmt.Errorf("remote queue enabled but url is empty (set [remote_queue].url or QI_QUEUE_URL)")
			}
			if cfg.RemoteQueue.Token == "" {
				return fmt.Errorf("remote queue enabled but token is empty (set [remote_queue].token or QI_QUEUE_TOKEN)")
			}

			client := remotequeue.NewClient(cfg.RemoteQueue.URL, cfg.RemoteQueue.Token)
			stats, err := client.Stats(cmd.Context())
			if err != nil {
				return err
			}

			fmt.Fprintf(out, "pending %d, deadletter %d", stats.Pending, stats.Deadletter)
			if stats.Deadletter > 0 {
				fmt.Fprint(out, " (see qi remote-drain --show-failed)")
			}
			fmt.Fprintln(out)
			return nil
		},
	}
	return cmd
}
