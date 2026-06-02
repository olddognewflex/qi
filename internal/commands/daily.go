package commands

import (
	"bufio"
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"qi/internal/ai"
	"qi/internal/config"
	"qi/internal/service"
	"qi/internal/vault"
)

func newDailyCommand(cfg config.Config) *cobra.Command {
	dailyCmd := &cobra.Command{
		Use:   "daily",
		Short: "Manage today's daily note (agenda, checkpoints, summary)",
	}

	dailyCmd.AddCommand(
		newDailyStartCommand(cfg),
		newDailyCpCommand(cfg),
		newDailyEndCommand(cfg),
	)
	return dailyCmd
}

func buildDailyService(cfg config.Config) service.DailyService {
	return service.DailyService{
		PathFor:    cfg.DailyNotePath,
		Agenda:     buildAgendaService(cfg),
		VaultName:  filepath.Base(cfg.VaultPath),
		OpenFunc:   openURL,
		CreateWait: 3 * time.Second,
	}
}

func newDailyStartCommand(cfg config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Open today's note and write the agenda into ## Agenda",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc := buildDailyService(cfg)
			today := time.Now()

			// Focus today's note in Obsidian (Advanced URI creates it via Templater).
			vaultName := filepath.Base(cfg.VaultPath)
			_ = openURL("obsidian://adv-uri?vault=" + url.QueryEscape(vaultName) + "&daily=true")

			path, err := svc.EnsureNote(today)
			if err != nil {
				return err
			}

			// Bring the note to the foreground.
			rel, relErr := filepath.Rel(cfg.VaultPath, path)
			if relErr == nil {
				_ = openURL("obsidian://open?vault=" + url.QueryEscape(vaultName) +
					"&file=" + url.QueryEscape(rel))
			}

			events, warnings, err := svc.Agenda.Today()
			if err != nil {
				return err
			}
			printAgendaWarnings(cmd, warnings)
			rendered := svc.RenderAgenda(events)
			if err := vault.ReplaceSection(path, "Agenda", rendered); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Wrote agenda (%d events) to %s\n", len(events), path)
			return nil
		},
	}
}

func newDailyCpCommand(cfg config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "cp <text...>",
		Short: "Append a timestamped checkpoint to ## Logs",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc := buildDailyService(cfg)
			path, err := svc.EnsureNote(time.Now())
			if err != nil {
				return err
			}

			text := strings.Join(args, " ")
			line := "- [" + time.Now().Format("15:04") + "] " + text
			if err := vault.AppendToSection(path, "Logs", line); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Logged: %s\n", text)
			return nil
		},
	}
}

func newDailyEndCommand(cfg config.Config) *cobra.Command {
	var socketFlag, providerFlag, modelFlag string
	cmd := &cobra.Command{
		Use:   "end [date]",
		Short: "Summarize a day's ## Logs via the AI planner (confirm before write)",
		Long: "Summarize the ## Logs of a daily note via the AI planner, then " +
			"(on confirmation) write the result to ## Summary.\n\n" +
			"Defaults to today. Pass a date as YYYY-MM-DD to summarize an earlier day.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			day, err := parseDailyDate(args, time.Now())
			if err != nil {
				return err
			}
			path := cfg.DailyNotePath(day)

			logs, found, err := vault.ReadSection(path, "Logs")
			if err != nil {
				return err
			}
			if !found || strings.TrimSpace(logs) == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "nothing to summarize")
				return nil
			}

			c, err := dialClient(socketFlag)
			if err != nil {
				return err
			}
			defer c.Close()

			planner, err := buildPlanner(c, providerFlag, modelFlag)
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			res, err := planner.Run(ctx, "Summarize these daily logs concisely:\n"+logs)
			if err != nil {
				return err
			}

			summary := collectPlannerText(res.Turns)
			if strings.TrimSpace(summary) == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "planner returned no summary")
				return nil
			}

			out := cmd.OutOrStdout()
			fmt.Fprintln(out, summary)
			fmt.Fprint(out, "\nWrite this to ## Summary? [y/N] ")

			reader := bufio.NewReader(cmd.InOrStdin())
			answer, _ := reader.ReadString('\n')
			if strings.ToLower(strings.TrimSpace(answer)) != "y" {
				fmt.Fprintln(out, "aborted")
				return nil
			}

			if err := vault.ReplaceSection(path, "Summary", summary); err != nil {
				return err
			}
			fmt.Fprintf(out, "Wrote summary to %s\n", path)
			return nil
		},
	}
	cmd.Flags().StringVar(&socketFlag, "socket", "", "qid socket path override")
	cmd.Flags().StringVar(&providerFlag, "provider", "", "LLM provider: anthropic|ollama (overrides config)")
	cmd.Flags().StringVar(&modelFlag, "model", "", "model id (overrides provider default + config)")
	return cmd
}

// parseDailyDate resolves the day a daily command targets. With no args it
// returns fallback (typically time.Now()); with one arg it parses YYYY-MM-DD in
// the local zone, erroring on anything else.
func parseDailyDate(args []string, fallback time.Time) (time.Time, error) {
	if len(args) == 0 {
		return fallback, nil
	}
	parsed, err := time.ParseInLocation("2006-01-02", args[0], time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid date %q: expected YYYY-MM-DD", args[0])
	}
	return parsed, nil
}

func collectPlannerText(turns []ai.TurnEvent) string {
	var parts []string
	for _, t := range turns {
		if strings.TrimSpace(t.Text) != "" {
			parts = append(parts, strings.TrimSpace(t.Text))
		}
	}
	return strings.Join(parts, "\n")
}
