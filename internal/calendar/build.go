package calendar

import (
	"errors"
	"fmt"

	"qi/internal/config"
)

// BuildWarning reports one configured calendar entry that could not be turned
// into a Provider — a missing CalDAV password, an unreadable vdir root. It is
// non-fatal: the rest of the calendars are still built.
//
// This is deliberately returned rather than printed. This package is a library
// with two callers that report differently: `qi agenda` writes to stderr, qid
// logs through its *slog.Logger. Error() renders the CLI's historical wording so
// the command layer can print the warning verbatim.
type BuildWarning struct {
	Kind string // config section kind: "caldav", "vdir"
	Name string // the entry's configured name
	Err  error
}

func (w BuildWarning) Error() string {
	return fmt.Sprintf("skipping %s %q: %v", w.Kind, w.Name, w.Err)
}

func (w BuildWarning) Unwrap() error { return w.Err }

// ProviderBuilder builds the calendar providers described by a config.Config.
// The zero value is ready to use and is what both call sites want; the fields
// exist only as seams for tests, so a unit test never has to touch the keychain.
type ProviderBuilder struct {
	// CalDAVPassword resolves a CalDAV entry's password. nil means
	// ResolveCalDAVPassword (config value, else the system keychain).
	CalDAVPassword func(config.CalDAVCalendar) (string, error)
}

// BuildProviders wires every calendar kind in cfg into providers. It is the one
// place calendar sources are assembled: `qi agenda` and qid both call it, so a
// newly supported calendar kind reaches the CLI, the agenda.today builtin, the
// skills, `qi ai run` and MCP clients at once.
//
// The local daily-notes provider is always present. Every other kind is driven
// purely by cfg. Entries that cannot be built yield a BuildWarning instead of an
// error or a silent drop — one dead calendar must not sink the agenda.
//
// When adding a calendar kind, wire it here *and* add it to the registry in
// TestBuildProvidersCoversEveryConfiguredCalendarKind (build_test.go).
func BuildProviders(cfg config.Config) ([]Provider, []BuildWarning) {
	return ProviderBuilder{}.Build(cfg)
}

// Build is BuildProviders with the builder's test seams applied.
func (b ProviderBuilder) Build(cfg config.Config) ([]Provider, []BuildWarning) {
	var warnings []BuildWarning
	providers := []Provider{
		LocalProvider{PathFor: cfg.DailyNotePath},
	}

	for _, cal := range cfg.ICSCalendars {
		providers = append(providers, ICSProvider{
			CalName: cal.Name,
			URL:     cal.URL,
		})
	}

	for _, cal := range cfg.CalDAVCalendars {
		pw, err := b.caldavPassword(cal)
		if err != nil {
			warnings = append(warnings, BuildWarning{Kind: "caldav", Name: cal.Name, Err: err})
			continue
		}
		providers = append(providers, CalDAVProvider{
			CalName:  cal.Name,
			Endpoint: cal.Endpoint,
			Username: cal.Username,
			Password: pw,
			Path:     cal.Path,
		})
	}

	for _, cal := range cfg.VdirCalendars {
		if !cal.Discover {
			providers = append(providers, VdirProvider{
				CalName: cal.Name,
				Path:    cal.Path,
			})
			continue
		}
		discovered, err := DiscoverVdir(cal.Path)
		if err != nil {
			warnings = append(warnings, BuildWarning{Kind: "vdir", Name: cal.Name, Err: err})
			continue
		}
		providers = append(providers, discovered...)
	}

	if cfg.GoogleOAuth.ClientID != "" && cfg.GoogleOAuth.ClientSecret != "" {
		oauthCfg := GoogleOAuthConfig(cfg.GoogleOAuth.ClientID, cfg.GoogleOAuth.ClientSecret, "")
		for _, cal := range cfg.GoogleCalendars {
			providers = append(providers, GoogleProvider{
				CalName:     cal.Name,
				Account:     cal.Account,
				CalendarID:  cal.CalendarID,
				OAuthConfig: oauthCfg,
			})
		}
	}

	return providers, warnings
}

func (b ProviderBuilder) caldavPassword(cal config.CalDAVCalendar) (string, error) {
	if b.CalDAVPassword != nil {
		return b.CalDAVPassword(cal)
	}
	return ResolveCalDAVPassword(cal)
}

// ResolveCalDAVPassword returns the CalDAV entry's password: the config value if
// set, else the system keychain entry stored by `qi calendar caldav-passwd`.
func ResolveCalDAVPassword(cal config.CalDAVCalendar) (string, error) {
	if cal.Password != "" {
		return cal.Password, nil
	}
	pw, err := GetCalDAVPassword(cal.Name)
	if err != nil {
		if errors.Is(err, ErrSecretNotFound) {
			return "", fmt.Errorf("no password in config and none in keychain (run `qi calendar caldav-passwd %s`)", cal.Name)
		}
		return "", err
	}
	return pw, nil
}
