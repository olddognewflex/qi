package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"qi/internal/calendar"
	"qi/internal/config"
	"qi/internal/domain"
	"qi/internal/service"
	"qi/internal/vault"
)

func newPlanCommand(cfg config.Config) *cobra.Command {
	var (
		startFlag   string
		blockFlag   string
		limitFlag   int
		projectFlag string
		allFlag     bool
		dryRun      bool
	)
	cmd := &cobra.Command{
		Use:   "plan [date]",
		Short: "Auto-time-block open tasks into the daily note's ## Schedule",
		Long: "Pack open tasks into free slots of a daily note's ## Schedule, so they " +
			"appear in qi agenda via the local calendar provider.\n\n" +
			"Defaults to today and to tasks due/scheduled that day; pass a date as " +
			"YYYY-MM-DD to plan an earlier day, or --all to include every open task. " +
			"Existing schedule entries are preserved and already-scheduled titles are " +
			"skipped, so re-running plan is idempotent.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			day, err := parseDailyDate(args, time.Now())
			if err != nil {
				return err
			}

			startMin, err := parseStartMinutes(startFlag)
			if err != nil {
				return err
			}
			blockDur, err := time.ParseDuration(blockFlag)
			if err != nil {
				return fmt.Errorf("invalid --block %q: %w", blockFlag, err)
			}
			blockMin := int(blockDur.Minutes())
			if blockMin <= 0 {
				return fmt.Errorf("invalid --block %q: must be greater than zero", blockFlag)
			}

			tasks := service.TaskService{
				TaskFilePath: cfg.TaskFilePath,
				TasksDir:     filepath.Dir(cfg.TaskFilePath),
			}
			open, err := tasks.ListOpenTasks()
			if err != nil {
				return err
			}

			candidates := filterPlanCandidates(open, projectFlag, allFlag, day)

			path := cfg.DailyNotePath(day)
			body, _, err := vault.ReadSection(path, "Schedule")
			if err != nil {
				return err
			}
			existing := calendar.ParseScheduleEntries(body)

			planned := service.PlanBlocks(service.PlanInput{
				Tasks:    candidates,
				Existing: existing,
				StartMin: startMin,
				BlockMin: blockMin,
				Limit:    limitFlag,
			})
			newBody := service.MergeAndRender(existing, planned)

			out := cmd.OutOrStdout()
			skipped := len(candidates) - len(planned)

			if dryRun {
				fmt.Fprintln(out, "## Schedule")
				fmt.Fprintln(out, newBody)
				fmt.Fprintf(out, "\n%d task(s) planned (%d already scheduled / skipped)\n", len(planned), skipped)
				return nil
			}

			if len(candidates) == 0 {
				fmt.Fprintf(out, "No tasks to plan for %s.\n", day.Format("2006-01-02"))
				return nil
			}

			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return fmt.Errorf("create daily dir: %w", err)
			}
			if err := vault.ReplaceSection(path, "Schedule", newBody); err != nil {
				return err
			}
			fmt.Fprintf(out, "Planned %d task(s) into %s\n", len(planned), path)
			return nil
		},
	}
	cmd.Flags().StringVar(&startFlag, "start", "09:00", "earliest time to plan into (HH:MM)")
	cmd.Flags().StringVar(&blockFlag, "block", "30m", "block length per task (Go duration, e.g. 30m)")
	cmd.Flags().IntVar(&limitFlag, "limit", 6, "maximum number of tasks to plan")
	cmd.Flags().StringVarP(&projectFlag, "project", "p", "", "only plan tasks with this project tag")
	cmd.Flags().BoolVar(&allFlag, "all", false, "include all open tasks (not just those due/scheduled that day)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the would-be ## Schedule without writing")
	return cmd
}

// filterPlanCandidates narrows open tasks to the planning set: a --project tag
// filter (when set), then a day filter unless --all is passed. Order is
// preserved throughout.
func filterPlanCandidates(tasks []domain.Task, project string, all bool, day time.Time) []domain.Task {
	if project != "" {
		filtered := make([]domain.Task, 0, len(tasks))
		for _, t := range tasks {
			if t.Project == project {
				filtered = append(filtered, t)
			}
		}
		tasks = filtered
	}
	if !all {
		tasks = service.FilterForDay(tasks, day)
	}
	return tasks
}

// parseStartMinutes parses an "HH:MM" start flag into minutes since midnight.
func parseStartMinutes(hhmm string) (int, error) {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return 0, fmt.Errorf("invalid --start %q: expected HH:MM", hhmm)
	}
	return t.Hour()*60 + t.Minute(), nil
}
