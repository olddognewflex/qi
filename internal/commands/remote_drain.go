package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"qi/internal/config"
	"qi/internal/remotequeue"
	"qi/internal/service"
)

// queueAdapter bridges the remotequeue HTTP client to the service.RemoteQueue
// interface, translating the wire Task into service.RemoteTask. This keeps the
// service layer free of HTTP/config dependencies.
type queueAdapter struct {
	c *remotequeue.Client
}

func (a queueAdapter) Pull(ctx context.Context, limit int) ([]service.RemoteTask, error) {
	tasks, err := a.c.Pull(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]service.RemoteTask, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, service.RemoteTask{
			ID:        t.ID,
			Text:      t.Text,
			Project:   t.Project,
			Client:    t.Client,
			Due:       t.Due,
			Scheduled: t.Scheduled,
		})
	}
	return out, nil
}

func (a queueAdapter) Ack(ctx context.Context, ids []string) error {
	return a.c.Ack(ctx, ids)
}

func (a queueAdapter) Deadletter(ctx context.Context, ids []string, reason string) error {
	return a.c.Deadletter(ctx, ids, reason)
}

func newRemoteDrainCommand(cfg config.Config) *cobra.Command {
	var showFailed bool
	var limit int

	cmd := &cobra.Command{
		Use:   "remote-drain",
		Short: "Drain remote-captured tasks from the cloud queue into the vault",
		Long: "Pull tasks from the cloud queue, validate each, write the valid ones\n" +
			"idempotently into the vault, ack what was written, and deadletter what failed.\n" +
			"A no-op when [remote_queue] is disabled. Off the hot path — intended for a\n" +
			"periodic launchd timer.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cfg.RemoteQueue.Enabled {
				fmt.Fprintln(os.Stdout, "remote queue disabled (set [remote_queue].enabled or QI_QUEUE_ENABLED); nothing to drain.")
				return nil
			}
			if cfg.RemoteQueue.URL == "" {
				return fmt.Errorf("remote queue enabled but url is empty (set [remote_queue].url or QI_QUEUE_URL)")
			}
			if cfg.RemoteQueue.Token == "" {
				return fmt.Errorf("remote queue enabled but token is empty (set [remote_queue].token or QI_QUEUE_TOKEN)")
			}

			client := remotequeue.NewClient(cfg.RemoteQueue.URL, cfg.RemoteQueue.Token)

			if showFailed {
				return printDeadletter(cmd.Context(), client)
			}

			taskSvc := service.TaskService{
				TaskFilePath: cfg.TaskFilePath,
				TasksDir:     filepath.Dir(cfg.TaskFilePath),
			}
			drain := service.DrainService{
				Tasks:    taskSvc,
				Queue:    queueAdapter{c: client},
				IsClient: func(name string) bool { _, ok := cfg.ClientByName(name); return ok },
			}

			summary, err := drain.Drain(cmd.Context(), limit)
			if err != nil {
				return err
			}

			fmt.Fprintf(os.Stdout, "drained %d, rejected %d", summary.Drained, summary.Rejected)
			if summary.Rejected > 0 {
				fmt.Fprint(os.Stdout, " (see qi remote-drain --show-failed)")
			}
			fmt.Fprintln(os.Stdout)
			return nil
		},
	}

	cmd.Flags().BoolVar(&showFailed, "show-failed", false, "list deadlettered tasks instead of draining")
	cmd.Flags().IntVar(&limit, "limit", 100, "max tasks to pull in one pass")
	return cmd
}

func printDeadletter(ctx context.Context, client *remotequeue.Client) error {
	tasks, err := client.ListDeadletter(ctx)
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		fmt.Fprintln(os.Stdout, "No deadlettered tasks.")
		return nil
	}
	for _, t := range tasks {
		line := fmt.Sprintf("%s  %s", t.ID, t.Text)
		if t.Reason != "" {
			line += fmt.Sprintf("  — %s", t.Reason)
		}
		fmt.Fprintln(os.Stdout, line)
	}
	return nil
}
