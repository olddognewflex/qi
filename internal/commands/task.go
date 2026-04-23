package commands

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"qi/internal/config"
	"qi/internal/domain"
	"qi/internal/service"
)

func newTaskCommand(cfg config.Config) *cobra.Command {
	svc := service.TaskService{TaskFilePath: cfg.TaskFilePath}

	taskCmd := &cobra.Command{
		Use:   "task",
		Short: "Manage tasks",
	}

	var project string
	var due string

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
			return svc.AddTask(service.AddTaskInput{
				Text:    args[0],
				Project: project,
				Due:     parsedDue,
			})
		},
	}
	addCmd.Flags().StringVarP(&project, "project", "p", "", "project tag")
	addCmd.Flags().StringVarP(&due, "due", "d", "", "due date (YYYY-MM-DD)")

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List open tasks",
		RunE: func(cmd *cobra.Command, args []string) error {
			tasks, err := svc.ListOpenTasks()
			if err != nil {
				return err
			}
			for _, task := range tasks {
				if task.Due != nil {
					fmt.Fprintf(os.Stdout, "- %s (due %s)\n", task.Text, task.Due.Format("2006-01-02"))
				} else {
					fmt.Fprintf(os.Stdout, "- %s\n", task.Text)
				}
			}
			return nil
		},
	}

	doneCmd := &cobra.Command{
		Use:   "done [fuzzy]",
		Short: "Mark a task as done",
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

			reader := bufio.NewReader(os.Stdin)
			var selected domain.Task

			if len(candidates) == 1 && query != "" {
				selected = candidates[0]
				fmt.Fprintf(os.Stdout, "Found:\n  %s\n\nMark as done? [y/N] ", taskDisplayLine(selected))
				response, _ := reader.ReadString('\n')
				if strings.ToLower(strings.TrimSpace(response)) != "y" {
					fmt.Fprintln(os.Stdout, "Aborted.")
					return nil
				}
			} else {
				if query != "" {
					fmt.Fprintf(os.Stdout, "Multiple tasks match %q:\n", query)
				} else {
					fmt.Fprintln(os.Stdout, "Open tasks:")
				}
				for i, t := range candidates {
					fmt.Fprintf(os.Stdout, "  %d. %s\n", i+1, taskDisplayLine(t))
				}
				fmt.Fprintf(os.Stdout, "\nSelect [1-%d]: ", len(candidates))
				line, _ := reader.ReadString('\n')
				n, convErr := strconv.Atoi(strings.TrimSpace(line))
				if convErr != nil || n < 1 || n > len(candidates) {
					return fmt.Errorf("invalid selection")
				}
				selected = candidates[n-1]
			}

			if err := svc.CompleteTask(selected); err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "✓ Done: %s\n", selected.Text)
			return nil
		},
	}

	taskCmd.AddCommand(addCmd, listCmd, doneCmd)
	return taskCmd
}

func taskDisplayLine(t domain.Task) string {
	if t.Due != nil {
		return fmt.Sprintf("%s (due %s)", t.Text, t.Due.Format("2006-01-02"))
	}
	return t.Text
}
