package commands

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"qi/internal/calendar"
	"qi/internal/config"
)

func readPassword(fd int) ([]byte, error) {
	return term.ReadPassword(fd)
}

func newCalendarCommand(cfg config.Config) *cobra.Command {
	calCmd := &cobra.Command{
		Use:   "calendar",
		Short: "Manage calendar accounts (OAuth, list calendars)",
	}

	noBrowser := false

	authCmd := &cobra.Command{
		Use:   "auth",
		Short: "Authorize a Google account via OAuth",
		Long:  "Runs the Google OAuth flow and saves a refresh token for the authorized account. The returned account email becomes the account ID used in [[google_calendars]] config entries.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if cfg.GoogleOAuth.ClientID == "" || cfg.GoogleOAuth.ClientSecret == "" {
				return fmt.Errorf("google_oauth.client_id and google_oauth.client_secret must be set in config.toml")
			}
			res, err := calendar.RunOAuthFlow(cmd.Context(), cfg.GoogleOAuth.ClientID, cfg.GoogleOAuth.ClientSecret, !noBrowser)
			if err != nil {
				return err
			}
			if err := calendar.SaveToken(res.Account, res.Token); err != nil {
				return fmt.Errorf("save token: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Authorized: %s\n", res.Account)
			fmt.Fprintf(cmd.OutOrStdout(), "Run `qi calendar list %s` to see available calendars.\n", res.Account)
			return nil
		},
	}
	authCmd.Flags().BoolVar(&noBrowser, "no-browser", false, "do not auto-open browser; only print the URL")

	listCmd := &cobra.Command{
		Use:   "list <account>",
		Short: "List Google calendars available for an authorized account",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			account := args[0]
			if cfg.GoogleOAuth.ClientID == "" || cfg.GoogleOAuth.ClientSecret == "" {
				return fmt.Errorf("google_oauth.client_id and google_oauth.client_secret must be set in config.toml")
			}
			oauthCfg := calendar.GoogleOAuthConfig(cfg.GoogleOAuth.ClientID, cfg.GoogleOAuth.ClientSecret, "")
			ctx := context.Background()
			svc, err := calendar.NewGoogleService(ctx, oauthCfg, account)
			if err != nil {
				return err
			}
			res, err := svc.CalendarList.List().Context(ctx).Do()
			if err != nil {
				return fmt.Errorf("calendarList: %w", err)
			}
			for _, c := range res.Items {
				marker := ""
				if c.Primary {
					marker = " (primary)"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "  %s  %s%s\n", c.Id, c.Summary, marker)
			}
			return nil
		},
	}

	accountsCmd := &cobra.Command{
		Use:   "accounts",
		Short: "List authorized Google accounts",
		RunE: func(cmd *cobra.Command, args []string) error {
			accounts, err := calendar.ListAuthorizedAccounts()
			if err != nil {
				return err
			}
			if len(accounts) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No authorized accounts. Run `qi calendar auth`.")
				return nil
			}
			for _, a := range accounts {
				fmt.Fprintln(cmd.OutOrStdout(), "  "+a)
			}
			return nil
		},
	}

	caldavListCmd := &cobra.Command{
		Use:   "caldav-list <name>",
		Short: "Discover CalDAV calendar paths for a configured CalDAV account",
		Long:  "Connects to the CalDAV account whose [[caldav_calendars]] entry has the matching name, then lists each calendar's path. Copy a path into the `path` field of a [[caldav_calendars]] entry to subscribe to that specific calendar.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			target := findCalDAVEntry(cfg, name)
			if target == nil {
				return fmt.Errorf("no [[caldav_calendars]] entry named %q in config.toml", name)
			}
			pw, err := resolveCalDAVPassword(*target)
			if err != nil {
				return err
			}
			provider := calendar.CalDAVProvider{
				CalName:  target.Name,
				Endpoint: target.Endpoint,
				Username: target.Username,
				Password: pw,
			}
			cals, err := provider.ListCalendars(cmd.Context())
			if err != nil {
				return err
			}
			for _, c := range cals {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s  %s\n", c.Path, c.Name)
			}
			return nil
		},
	}

	caldavPasswdCmd := &cobra.Command{
		Use:   "caldav-passwd <name>",
		Short: "Store the password for a CalDAV account in the system keychain",
		Long:  "Prompts for a password (input hidden) and stores it in the OS keychain under service `qi-caldav`, account `<name>`. After storing, the `password` field can be removed from [[caldav_calendars]] in config.toml.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if findCalDAVEntry(cfg, name) == nil {
				return fmt.Errorf("no [[caldav_calendars]] entry named %q in config.toml", name)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Password for %s (input hidden): ", name)
			pw, err := readPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(cmd.OutOrStdout())
			if err != nil {
				return fmt.Errorf("read password: %w", err)
			}
			if len(pw) == 0 {
				return fmt.Errorf("empty password")
			}
			if err := calendar.SetCalDAVPassword(name, string(pw)); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Stored password for %s in keychain.\n", name)
			return nil
		},
	}

	caldavForgetCmd := &cobra.Command{
		Use:   "caldav-forget <name>",
		Short: "Remove the keychain entry for a CalDAV account",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := calendar.DeleteCalDAVPassword(name); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed keychain entry for %s.\n", name)
			return nil
		},
	}

	calCmd.AddCommand(authCmd, listCmd, accountsCmd, caldavListCmd, caldavPasswdCmd, caldavForgetCmd)
	return calCmd
}

func findCalDAVEntry(cfg config.Config, name string) *config.CalDAVCalendar {
	for i := range cfg.CalDAVCalendars {
		if cfg.CalDAVCalendars[i].Name == name {
			return &cfg.CalDAVCalendars[i]
		}
	}
	return nil
}

func resolveCalDAVPassword(cal config.CalDAVCalendar) (string, error) {
	if cal.Password != "" {
		return cal.Password, nil
	}
	pw, err := calendar.GetCalDAVPassword(cal.Name)
	if err != nil {
		if errors.Is(err, calendar.ErrSecretNotFound) {
			return "", fmt.Errorf("no password in config and none in keychain (run `qi calendar caldav-passwd %s`)", cal.Name)
		}
		return "", err
	}
	return pw, nil
}
