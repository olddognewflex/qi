package config

// Calendar configuration: the public provider config types and their TOML wire
// twins. Parsing/normalization stays inline in Load (config.go); this file holds
// the declarations, split out of the former config.go god-file (#60).

type ICSCalendar struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type CalDAVCalendar struct {
	Name     string
	Endpoint string
	Username string
	Password string
	Path     string
}

// VdirCalendar is a vdir-format calendar on disk, as synced by vdirsyncer.
// Path is a single collection directory, or — when Discover is set — a root
// holding one directory per collection (khal's `type = discover`).
type VdirCalendar struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Discover bool   `json:"discover"`
}

type GoogleOAuth struct {
	ClientID     string
	ClientSecret string
}

type GoogleCalendar struct {
	Name       string `json:"name"`
	Account    string `json:"account"`
	CalendarID string `json:"calendar_id"`
}

type icsCalTOML struct {
	Name string `toml:"name"`
	URL  string `toml:"url"`
}

type caldavCalTOML struct {
	Name     string `toml:"name"`
	Endpoint string `toml:"endpoint"`
	Username string `toml:"username"`
	Email    string `toml:"email"` // alias for username (legacy Google-only configs)
	Password string `toml:"password"`
	Path     string `toml:"path"`
}

type vdirCalTOML struct {
	Name     string `toml:"name"`
	Path     string `toml:"path"`
	Discover bool   `toml:"discover"` // path is a root of collections, not one collection
}

type googleOAuthTOML struct {
	ClientID     string `toml:"client_id"`
	ClientSecret string `toml:"client_secret"`
}

type googleCalTOML struct {
	Name       string `toml:"name"`
	Account    string `toml:"account"`
	CalendarID string `toml:"calendar_id"`
}
