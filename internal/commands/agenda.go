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
			events, err := svc.Today()
			if err != nil {
				return err
			}
			printEvents(cmd, events, false)
			return nil
		},
	}

	weekCmd := &cobra.Command{
		Use:   "week",
		Short: "Show this week's events",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc := buildAgendaService(cfg)
			events, err := svc.Week()
			if err != nil {
				return err
			}
			printEvents(cmd, events, true)
			return nil
		},
	}

	agendaCmd.RunE = todayCmd.RunE
	agendaCmd.AddCommand(todayCmd, weekCmd)
	return agendaCmd
}

func buildAgendaService(cfg config.Config) service.AgendaService {
	providers := []calendar.Provider{
		calendar.LocalProvider{DailyDir: cfg.DailyPath},
	}

	for _, cal := range cfg.ICSCalendars {
		providers = append(providers, calendar.ICSProvider{
			CalName: cal.Name,
			URL:     cal.URL,
		})
	}

	for _, cal := range cfg.CalDAVCalendars {
		pw, err := resolveCalDAVPassword(cal)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skipping caldav %q: %v\n", cal.Name, err)
			continue
		}
		providers = append(providers, calendar.CalDAVProvider{
			CalName:  cal.Name,
			Endpoint: cal.Endpoint,
			Username: cal.Username,
			Password: pw,
			Path:     cal.Path,
		})
	}

	if cfg.GoogleOAuth.ClientID != "" && cfg.GoogleOAuth.ClientSecret != "" {
		oauthCfg := calendar.GoogleOAuthConfig(cfg.GoogleOAuth.ClientID, cfg.GoogleOAuth.ClientSecret, "")
		for _, cal := range cfg.GoogleCalendars {
			providers = append(providers, calendar.GoogleProvider{
				CalName:     cal.Name,
				Account:     cal.Account,
				CalendarID:  cal.CalendarID,
				OAuthConfig: oauthCfg,
			})
		}
	}

	return service.AgendaService{Providers: providers}
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




