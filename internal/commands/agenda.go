package commands

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"qi/internal/calendar"
	"qi/internal/config"
	"qi/internal/domain"
	"qi/internal/service"
)

func newAgendaCommand(cfg config.Config) *cobra.Command {
	agendaCmd := &cobra.Command{
		Use:   "agenda",
		Short: "Show calendar events",
	}

	todayCmd := &cobra.Command{
		Use:   "today",
		Short: "Show today's events",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc := buildAgendaService(cfg)
			events, warnings, err := svc.Today()
			if err != nil {
				return err
			}
			printAgendaWarnings(cmd, warnings)
			printEvents(cmd, events, false)
			return nil
		},
	}

	weekCmd := &cobra.Command{
		Use:   "week",
		Short: "Show this week's events",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc := buildAgendaService(cfg)
			events, warnings, err := svc.Week()
			if err != nil {
				return err
			}
			printAgendaWarnings(cmd, warnings)
			printEvents(cmd, events, true)
			return nil
		},
	}

	agendaCmd.RunE = todayCmd.RunE
	agendaCmd.AddCommand(todayCmd, weekCmd)
	return agendaCmd
}

// buildAgendaService wires the shared calendar provider set (calendar.BuildProviders
// — the same one qid uses) into an AgendaService, reporting entries that could not
// be built on stderr in the CLI's idiom.
func buildAgendaService(cfg config.Config) service.AgendaService {
	providers, warnings := calendar.BuildProviders(cfg)
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, w.Error())
	}
	return service.AgendaService{Providers: providers}
}

// printAgendaWarnings reports providers that failed so a dead calendar (e.g. an
// expired OAuth token) is visible instead of silently missing from the agenda.
func printAgendaWarnings(cmd *cobra.Command, warnings []service.ProviderWarning) {
	for _, w := range warnings {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: calendar %s\n", w.Error())
	}
}

func printEvents(cmd *cobra.Command, events []domain.Event, showDate bool) {
	if len(events) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No events.")
		return
	}

	var currentDay string
	for _, e := range events {
		if showDate {
			day := e.Start.Format("Mon Jan 02")
			if day != currentDay {
				currentDay = day
				fmt.Fprintln(cmd.OutOrStdout(), day)
			}
		}
		endStr := ""
		if !e.End.IsZero() && e.End != e.Start {
			endStr = "–" + e.End.Format("15:04")
		}
		source := ""
		if e.Source == domain.EventSourceLocal {
			source = " [local]"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  %s%s  %s%s\n", e.Start.Format("15:04"), endStr, e.Title, source)
	}
}
