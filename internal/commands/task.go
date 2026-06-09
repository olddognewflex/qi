package commands

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"qi/internal/config"
	"qi/internal/domain"
	"qi/internal/service"
	"qi/internal/tui"
)

func newTaskCommand(cfg config.Config) *cobra.Command {
	svc := service.TaskService{
		TaskFilePath: cfg.TaskFilePath,
		TasksDir:     filepath.Dir(cfg.TaskFilePath),
	}

	taskCmd := &cobra.Command{
		Use:   "task",
		Short: "Manage tasks",
	}

	var project string
	var client string
	var due string
	var schedule string

	addCmd := &cobra.Command{
		Use:   "add <text>",
		Short: "Add a task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var parsedDue *time.Time
			if due != "" {
				t, err := time.Parse("2006-01-02", due)
				if err != nil {
					return fmt.Errorf("invalid due date, use YYYY-MM-DD: %w", err)
				}
				parsedDue = &t
			}

			var parsedSchedule *time.Time
			if schedule != "" {
				t, err := time.Parse("2006-01-02", schedule)
				if err != nil {
					return fmt.Errorf("invalid schedule date, use YYYY-MM-DD: %w", err)
				}
				parsedSchedule = &t
			}

			// --client tags the task with a configured client name (validated),
			// routing it to that client's task_file via sync. Mutually exclusive
			// with the free-form --project tag.
			tag := project
			if client != "" {
				if project != "" {
					return fmt.Errorf("--client and --project are mutually exclusive")
				}
				if _, ok := cfg.ClientByName(client); !ok {
					return fmt.Errorf("unknown client %q", client)
				}
				tag = client
			}

			return svc.AddTask(service.AddTaskInput{
				Text:      args[0],
				Project:   tag,
				Due:       parsedDue,
				Scheduled: parsedSchedule,
			})
		},
	}
	addCmd.Flags().StringVarP(&project, "project", "p", "", "project tag")
	addCmd.Flags().StringVarP(&client, "client", "c", "", "client name (routes to the client's task_file)")
	addCmd.Flags().StringVarP(&due, "due", "d", "", "due date (YYYY-MM-DD)")
	addCmd.Flags().StringVarP(&schedule, "schedule", "s", "", "scheduled date (YYYY-MM-DD)")

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List open tasks",
		RunE: func(cmd *cobra.Command, args []string) error {
			tasks, err := svc.ListOpenTasks()
			if err != nil {
				return err
			}
			for _, task := range tasks {
				fmt.Fprintf(os.Stdout, "- %s\n", taskDisplayLine(task))
			}
			return nil
		},
	}

	doneCmd := &cobra.Command{
		Use:   "done [fuzzy]",
		Short: "Mark task(s) as done",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tasks, err := svc.ListOpenTasks()
			if err != nil {
				return err
			}
			if len(tasks) == 0 {
				fmt.Fprintln(os.Stdout, "No open tasks.")
				return nil
			}

			query := ""
			if len(args) > 0 {
				query = args[0]
			}

			candidates := service.FuzzyMatch(query, tasks)
			if len(candidates) == 0 {
				fmt.Fprintf(os.Stdout, "No tasks match %q.\n", query)
				return nil
			}

			// Exact-one match with query keeps quick y/N flow.
			if len(candidates) == 1 && query != "" {
				selected := candidates[0]
				reader := bufio.NewReader(os.Stdin)
				fmt.Fprintf(os.Stdout, "Found:\n  %s\n\nMark as done? [y/N] ", taskDisplayLine(selected))
				response, _ := reader.ReadString('\n')
				if strings.ToLower(strings.TrimSpace(response)) != "y" {
					fmt.Fprintln(os.Stdout, "Aborted.")
					return nil
				}
				if err := svc.CompleteTask(selected); err != nil {
					return err
				}
				fmt.Fprintf(os.Stdout, "✓ Done: %s\n", selected.Text)
				return nil
			}

			title := "Select tasks to complete"
			if query != "" {
				title = fmt.Sprintf("Tasks matching %q", query)
			}
			picked, err := tui.PickTasks(title, candidates)
			if err != nil {
				return err
			}
			if len(picked) == 0 {
				fmt.Fprintln(os.Stdout, "Aborted.")
				return nil
			}

			completed := completeTasks(svc, picked)
			for _, t := range completed {
				fmt.Fprintf(os.Stdout, "✓ Done: %s\n", t.Text)
			}
			return nil
		},
	}

	taskCmd.AddCommand(addCmd, listCmd, doneCmd)
	return taskCmd
}

func completeTasks(svc service.TaskService, tasks []domain.Task) []domain.Task {
	done := make([]domain.Task, 0, len(tasks))
	for _, t := range tasks {
		if err := svc.CompleteTask(t); err != nil {
			fmt.Fprintf(os.Stderr, "skip %q: %v\n", t.Text, err)
			continue
		}
		done = append(done, t)
	}
	return done
}

func taskDisplayLine(t domain.Task) string {
	var meta []string
	if t.Scheduled != nil {
		meta = append(meta, "scheduled "+t.Scheduled.Format("2006-01-02"))
	}
	if t.Due != nil {
		meta = append(meta, "due "+t.Due.Format("2006-01-02"))
	}
	if len(meta) == 0 {
		return t.Text
	}
	return fmt.Sprintf("%s (%s)", t.Text, strings.Join(meta, ", "))
}
