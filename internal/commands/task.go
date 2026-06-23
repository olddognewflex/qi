package commands

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"qi/internal/ai"
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
	var repeat string
	var breakdown string

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

			input := service.AddTaskInput{
				Text:       args[0],
				Project:    tag,
				Due:        parsedDue,
				Scheduled:  parsedSchedule,
				Recurrence: repeat,
			}

			// --breakdown opts into an AI subtask split. Without it, add stays
			// on its zero-AI hot path (byte-for-byte unchanged). With it, the
			// base task is created first (so it persists even if the AI is
			// unreachable), then its proposed subtasks are confirmation-gated
			// before any are written.
			if !cmd.Flags().Changed("breakdown") {
				return svc.AddTask(input)
			}
			if !ai.ValidLevel(breakdown) {
				return fmt.Errorf("invalid --breakdown level %q (want coarse|normal|fine)", breakdown)
			}
			llm, model, err := buildLLM("", "")
			if err != nil {
				return err
			}
			parent, err := svc.CreateTask(input)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "Added: %s\n", parent.Text)
			return breakdownFn(svc, parent, breakdown, llm, model, false, os.Stdin, os.Stdout)
		},
	}
	addCmd.Flags().StringVarP(&project, "project", "p", "", "project tag")
	addCmd.Flags().StringVarP(&client, "client", "c", "", "client name (routes to the client's task_file)")
	addCmd.Flags().StringVarP(&due, "due", "d", "", "due date (YYYY-MM-DD)")
	addCmd.Flags().StringVarP(&schedule, "schedule", "s", "", "scheduled date (YYYY-MM-DD)")
	addCmd.Flags().StringVarP(&repeat, "repeat", "r", "", "recurrence rule, e.g. \"every week\"")
	addCmd.Flags().StringVar(&breakdown, "breakdown", "", "AI-split the task into subtasks at an optional level (coarse|normal|fine; default normal)")
	// NoOptDefVal lets `--breakdown` (no value) resolve to the default level
	// while `--breakdown=fine` sets an explicit one.
	addCmd.Flags().Lookup("breakdown").NoOptDefVal = ai.DefaultBreakdownLevel

	var listProject, listStatus, listDate, listBefore, listAfter string

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List tasks (filter by project, status, and/or date)",
		RunE: func(cmd *cobra.Command, args []string) error {
			filter := service.TaskFilter{
				Project: listProject,
				Status:  listStatus,
				Date:    listDate,
			}
			if listBefore != "" {
				t, err := parseScheduleDate(listBefore)
				if err != nil {
					return err
				}
				filter.Before = &t
			}
			if listAfter != "" {
				t, err := parseScheduleDate(listAfter)
				if err != nil {
					return err
				}
				filter.After = &t
			}

			tasks, err := svc.ListTasks(filter)
			if err != nil {
				return err
			}
			if len(tasks) == 0 {
				fmt.Fprintln(os.Stdout, "No matching tasks.")
				return nil
			}
			for _, task := range tasks {
				fmt.Fprintf(os.Stdout, "- %s\n", taskDisplayLine(task))
			}
			return nil
		},
	}
	listCmd.Flags().StringVarP(&listProject, "project", "p", "", "filter by project tag")
	listCmd.Flags().StringVarP(&listStatus, "status", "s", "open", "completion status: open, done, or all")
	listCmd.Flags().StringVar(&listDate, "date", "", "scheduled/due date: today, overdue, or YYYY-MM-DD")
	listCmd.Flags().StringVar(&listBefore, "before", "", "scheduled/due strictly before date (YYYY-MM-DD, today, tomorrow, +Nd)")
	listCmd.Flags().StringVar(&listAfter, "after", "", "scheduled/due strictly after date (YYYY-MM-DD, today, tomorrow, +Nd)")

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

	var schedProject string
	var schedDryRun bool

	scheduleCmd := &cobra.Command{
		Use:   "schedule [fuzzy] <date>",
		Short: "(Re)schedule task(s) to a date",
		Long: "Set the scheduled date (⏳) of one or more open tasks. With one argument\n" +
			"the date applies via the picker; with two, the first fuzzy-matches tasks.\n" +
			"Date accepts YYYY-MM-DD, \"today\", \"tomorrow\", or \"+Nd\".",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := ""
			dateArg := args[0]
			if len(args) == 2 {
				query = args[0]
				dateArg = args[1]
			}

			when, err := parseScheduleDate(dateArg)
			if err != nil {
				return err
			}

			tasks, err := svc.ListOpenTasks()
			if err != nil {
				return err
			}
			if schedProject != "" {
				tasks = filterByProject(tasks, schedProject)
				if len(tasks) == 0 {
					fmt.Fprintf(os.Stdout, "No open tasks for project %q.\n", schedProject)
					return nil
				}
			}
			if len(tasks) == 0 {
				fmt.Fprintln(os.Stdout, "No open tasks.")
				return nil
			}

			candidates := service.FuzzyMatch(query, tasks)
			if len(candidates) == 0 {
				fmt.Fprintf(os.Stdout, "No tasks match %q.\n", query)
				return nil
			}

			var picked []domain.Task
			if len(candidates) == 1 && query != "" {
				picked = candidates
			} else {
				title := "Select tasks to reschedule"
				if query != "" {
					title = fmt.Sprintf("Tasks matching %q", query)
				}
				picked, err = tui.PickTasks(title, candidates)
				if err != nil {
					return err
				}
				if len(picked) == 0 {
					fmt.Fprintln(os.Stdout, "Aborted.")
					return nil
				}
			}

			target := when.Format("2006-01-02")
			if schedDryRun {
				for _, t := range picked {
					fmt.Fprintf(os.Stdout, "would schedule: %s -> %s\n", t.Text, target)
				}
				fmt.Fprintf(os.Stdout, "\n%d task(s) would be rescheduled (dry-run, no writes)\n", len(picked))
				return nil
			}

			rescheduled := rescheduleTasks(svc, picked, when)
			for _, t := range rescheduled {
				fmt.Fprintf(os.Stdout, "⏳ Scheduled %s: %s\n", target, t.Text)
			}
			return nil
		},
	}
	scheduleCmd.Flags().StringVarP(&schedProject, "project", "p", "", "filter candidates by project tag")
	scheduleCmd.Flags().BoolVar(&schedDryRun, "dry-run", false, "preview changes without writing to the vault")

	taskCmd.AddCommand(addCmd, listCmd, doneCmd, scheduleCmd)
	return taskCmd
}

// rescheduleTasks sets the scheduled date of each task, skipping (with a stderr
// note) any that fail to write, and returns those successfully rescheduled.
func rescheduleTasks(svc service.TaskService, tasks []domain.Task, when time.Time) []domain.Task {
	done := make([]domain.Task, 0, len(tasks))
	for _, t := range tasks {
		w := when
		if err := svc.RescheduleTask(t, &w); err != nil {
			fmt.Fprintf(os.Stderr, "skip %q: %v\n", t.Text, err)
			continue
		}
		done = append(done, t)
	}
	return done
}

// filterByProject keeps tasks whose project tag matches name (case-insensitive).
func filterByProject(tasks []domain.Task, name string) []domain.Task {
	want := strings.ToLower(strings.TrimSpace(name))
	out := make([]domain.Task, 0, len(tasks))
	for _, t := range tasks {
		if strings.ToLower(t.Project) == want {
			out = append(out, t)
		}
	}
	return out
}

// parseScheduleDate parses a user date: YYYY-MM-DD, "today", "tomorrow", or "+Nd"
// (N days from today). Times are local; the time-of-day is zeroed.
func parseScheduleDate(s string) (time.Time, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	switch s {
	case "today":
		return today, nil
	case "tomorrow":
		return today.AddDate(0, 0, 1), nil
	}
	if strings.HasPrefix(s, "+") && strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(s, "+"), "d"))
		if err == nil {
			return today.AddDate(0, 0, n), nil
		}
	}
	parsed, err := time.ParseInLocation("2006-01-02", s, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid date %q: use YYYY-MM-DD, today, tomorrow, or +Nd", s)
	}
	return parsed, nil
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
