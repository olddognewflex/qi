package calendar

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"qi/internal/config"
)

// calendarKindFixture populates exactly one calendar kind on cfg with a single
// minimally-valid entry, plus whatever else that kind needs to be buildable
// (Google, for instance, is skipped wholesale without OAuth credentials). It
// must not require the network, the keychain, or a real account: Build only
// constructs providers, it never fetches, so every fixture stays offline.
type calendarKindFixture func(t *testing.T, cfg *config.Config)

// calendarKindFixtures maps each config.Config field named *Calendars to its
// fixture. TestBuildProvidersCoversEveryConfiguredCalendarKind walks the struct
// by reflection and fails on any *Calendars field missing from this table, so a
// new calendar kind cannot be added to the config without being wired into
// BuildProviders.
var calendarKindFixtures = map[string]calendarKindFixture{
	"ICSCalendars": func(t *testing.T, cfg *config.Config) {
		cfg.ICSCalendars = []config.ICSCalendar{{
			Name: "ics-fixture",
			URL:  "https://example.invalid/cal.ics",
		}}
	},
	"CalDAVCalendars": func(t *testing.T, cfg *config.Config) {
		// Password inline: an empty one would fall through to the system
		// keychain, making the test depend on the developer's machine.
		cfg.CalDAVCalendars = []config.CalDAVCalendar{{
			Name:     "caldav-fixture",
			Endpoint: "https://example.invalid/dav",
			Username: "user",
			Password: "pw",
			Path:     "/dav/cal",
		}}
	},
	"VdirCalendars": func(t *testing.T, cfg *config.Config) {
		cfg.VdirCalendars = []config.VdirCalendar{{
			Name: "vdir-fixture",
			Path: t.TempDir(),
		}}
	},
	"GoogleCalendars": func(t *testing.T, cfg *config.Config) {
		// Google providers are skipped entirely without OAuth credentials, so the
		// fixture must supply them; they are never used to reach the network here.
		cfg.GoogleOAuth = config.GoogleOAuth{ClientID: "id", ClientSecret: "secret"}
		cfg.GoogleCalendars = []config.GoogleCalendar{{
			Name:       "google-fixture",
			Account:    "someone@example.invalid",
			CalendarID: "primary",
		}}
	},
}

// TestBuildProvidersCoversEveryConfiguredCalendarKind is the anti-drift guard.
//
// The drift this repo actually suffered was not two builders disagreeing (there
// is only one now) — it was a calendar kind reaching config.Config and the TOML
// while nobody wired it into the builder, leaving those calendars invisible.
// This test enumerates the *Calendars fields of config.Config by reflection and
// asserts each one, populated on its own, yields at least one provider beyond
// the always-present LocalProvider.
func TestBuildProvidersCoversEveryConfiguredCalendarKind(t *testing.T) {
	fields := calendarConfigFields()
	if len(fields) == 0 {
		t.Fatal("no *Calendars fields found on config.Config — has the config been restructured?")
	}

	for _, field := range fields {
		t.Run(field, func(t *testing.T) {
			fixture, ok := calendarKindFixtures[field]
			if !ok {
				t.Fatalf(missingFixtureMsg, field, field)
			}

			cfg := config.Config{VaultPath: t.TempDir()}
			fixture(t, &cfg)

			providers, warnings := BuildProviders(cfg)
			if len(warnings) != 0 {
				t.Fatalf("config.%s fixture produced warnings %v — fix the fixture so it builds cleanly", field, warnings)
			}
			// LocalProvider is unconditional; anything beyond it came from this kind.
			if len(providers) < 2 {
				t.Fatalf(unwiredKindMsg, field, providerNames(providers))
			}
		})
	}
}

const missingFixtureMsg = `config.Config has a calendar field %q with no fixture in calendarKindFixtures.

A new calendar kind was added to config.Config. Two things are needed:
  1. Wire it into ProviderBuilder.Build in internal/calendar/build.go, so it
     reaches BOTH ` + "`qi agenda`" + ` (CLI) and qid (agenda.today, the review
     skills, ` + "`qi ai run`" + `, MCP). Wiring only one is the drift this test exists
     to prevent.
  2. Add %q to calendarKindFixtures in this file, populating one minimally-valid
     entry (no network, no keychain, no real account).`

const unwiredKindMsg = `config.%s is populated but BuildProviders returned no provider for it (got only: %v).

The calendar kind exists in config.Config but is not wired into
ProviderBuilder.Build in internal/calendar/build.go — every calendar configured
under this section is silently invisible in ` + "`qi agenda`" + ` AND in everything
behind qid (agenda.today, the review skills, ` + "`qi ai run`" + `, MCP clients).
Add it to the builder.`

// calendarConfigFields returns the names of config.Config fields matching
// *Calendars — the sections that describe calendar sources.
func calendarConfigFields() []string {
	var out []string
	t := reflect.TypeOf(config.Config{})
	for i := range t.NumField() {
		name := t.Field(i).Name
		if name != "Calendars" && strings.HasSuffix(name, "Calendars") {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func providerNames(providers []Provider) []string {
	names := make([]string, 0, len(providers))
	for _, p := range providers {
		names = append(names, p.Name())
	}
	return names
}

func TestBuildProviders(t *testing.T) {
	vdirRoot := t.TempDir()
	for _, coll := range []string{"work", "personal"} {
		dir := filepath.Join(vdirRoot, coll)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Metadata gives one discovered collection a display name; the other falls
	// back to its directory name.
	if err := os.WriteFile(filepath.Join(vdirRoot, "work", "displayname"), []byte("Work Cal"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name         string
		cfg          config.Config
		builder      ProviderBuilder
		wantNames    []string
		wantWarnings []string
	}{
		{
			name:      "local only",
			cfg:       config.Config{},
			wantNames: []string{"local"},
		},
		{
			name: "several kinds at once",
			cfg: config.Config{
				ICSCalendars:    []config.ICSCalendar{{Name: "ics", URL: "https://example.invalid/c.ics"}},
				CalDAVCalendars: []config.CalDAVCalendar{{Name: "dav", Endpoint: "https://example.invalid", Username: "u", Password: "p"}},
				VdirCalendars:   []config.VdirCalendar{{Name: "vd", Path: t.TempDir()}},
				GoogleOAuth:     config.GoogleOAuth{ClientID: "id", ClientSecret: "secret"},
				GoogleCalendars: []config.GoogleCalendar{{Name: "goog", Account: "a@example.invalid", CalendarID: "primary"}},
			},
			wantNames: []string{"local", "ics", "dav", "vd", "goog"},
		},
		{
			name: "google skipped without oauth credentials",
			cfg: config.Config{
				GoogleCalendars: []config.GoogleCalendar{{Name: "goog", Account: "a@example.invalid", CalendarID: "primary"}},
			},
			wantNames: []string{"local"},
		},
		{
			name: "vdir discover yields one provider per collection",
			cfg: config.Config{
				VdirCalendars: []config.VdirCalendar{{Name: "root", Path: vdirRoot, Discover: true}},
			},
			wantNames: []string{"local", "Work Cal", "personal"},
		},
		{
			name: "vdir discover on a missing root warns",
			cfg: config.Config{
				VdirCalendars: []config.VdirCalendar{{Name: "gone", Path: filepath.Join(vdirRoot, "nope")}, {Name: "root", Path: filepath.Join(vdirRoot, "nope"), Discover: true}},
			},
			// The non-discover entry still builds: it only reads at Events time.
			wantNames:    []string{"local", "gone"},
			wantWarnings: []string{`skipping vdir "root":`},
		},
		{
			name: "unresolvable caldav password warns rather than dropping silently",
			cfg: config.Config{
				CalDAVCalendars: []config.CalDAVCalendar{
					{Name: "broken", Endpoint: "https://example.invalid"},
					{Name: "ok", Endpoint: "https://example.invalid", Password: "p"},
				},
			},
			builder: ProviderBuilder{CalDAVPassword: func(cal config.CalDAVCalendar) (string, error) {
				if cal.Password == "" {
					return "", errors.New("no password in config and none in keychain (run `qi calendar caldav-passwd broken`)")
				}
				return cal.Password, nil
			}},
			wantNames:    []string{"local", "ok"},
			wantWarnings: []string{`skipping caldav "broken": no password in config and none in keychain`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			providers, warnings := tt.builder.Build(tt.cfg)

			got := providerNames(providers)
			if !reflect.DeepEqual(got, tt.wantNames) {
				t.Errorf("providers = %v, want %v", got, tt.wantNames)
			}
			if len(warnings) != len(tt.wantWarnings) {
				t.Fatalf("warnings = %v, want %d", warnings, len(tt.wantWarnings))
			}
			for i, want := range tt.wantWarnings {
				if !strings.Contains(warnings[i].Error(), want) {
					t.Errorf("warning[%d] = %q, want it to contain %q", i, warnings[i].Error(), want)
				}
			}
		})
	}
}

// The CLI prints a BuildWarning verbatim; its wording is the user-facing text
// `qi agenda` has always shown for an unbuildable calendar.
func TestBuildWarningError(t *testing.T) {
	w := BuildWarning{Kind: "caldav", Name: "work", Err: fmt.Errorf("boom")}
	if got, want := w.Error(), `skipping caldav "work": boom`; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(error(w), w.Err) {
		t.Error("BuildWarning does not unwrap to its cause")
	}
}
