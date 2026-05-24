package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type ICSCalendar struct {
	Name string
	URL  string
}

type CalDAVCalendar struct {
	Name     string
	Endpoint string
	Username string
	Password string
	Path     string
}

type GoogleOAuth struct {
	ClientID     string
	ClientSecret string
}

type GoogleCalendar struct {
	Name       string
	Account    string
	CalendarID string
}

type MCPServer struct {
	ID      string
	Command string
	Args    []string
	Env     map[string]string
}

// AIConfig selects the LLM provider and per-provider defaults used by
// `qi ai run`. The provider string is matched case-insensitively against
// ai.ProviderAnthropic / ai.ProviderOllama.
type AIConfig struct {
	Provider    string
	Model       string
	OllamaURL   string
	OllamaModel string
}

type Config struct {
	VaultPath       string
	TaskFilePath    string
	InboxPath       string
	NotesPath       string
	DailyPath       string
	ICSCalendars    []ICSCalendar
	CalDAVCalendars []CalDAVCalendar
	GoogleOAuth     GoogleOAuth
	GoogleCalendars []GoogleCalendar
	MCPServers      []MCPServer
	AI              AIConfig
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

type googleOAuthTOML struct {
	ClientID     string `toml:"client_id"`
	ClientSecret string `toml:"client_secret"`
}

type googleCalTOML struct {
	Name       string `toml:"name"`
	Account    string `toml:"account"`
	CalendarID string `toml:"calendar_id"`
}

type mcpServerTOML struct {
	ID      string            `toml:"id"`
	Command string            `toml:"command"`
	Args    []string          `toml:"args"`
	Env     map[string]string `toml:"env"`
}

type aiTOML struct {
	Provider    string `toml:"provider"`
	Model       string `toml:"model"`
	OllamaURL   string `toml:"ollama_url"`
	OllamaModel string `toml:"ollama_model"`
}

type tomlFile struct {
	VaultPath       string          `toml:"vault_path"`
	TaskFilePath    string          `toml:"task_file_path"`
	ICSCalendars    []icsCalTOML    `toml:"ics_calendars"`
	CalDAVCalendars []caldavCalTOML `toml:"caldav_calendars"`
	GoogleOAuth     googleOAuthTOML `toml:"google_oauth"`
	GoogleCalendars []googleCalTOML `toml:"google_calendars"`
	MCPServers      []mcpServerTOML `toml:"mcp_servers"`
	AI              aiTOML          `toml:"ai"`
}

func ConfigPath() string {
	return filepath.Join(configDir(), "config.toml")
}

func configDir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "qi")
}

func Load() (Config, error) {
	return LoadFrom(ConfigPath())
}

func LoadFrom(path string) (Config, error) {
	var raw tomlFile

	if _, err := os.Stat(path); err == nil {
		if _, err := toml.DecodeFile(path, &raw); err != nil {
			return Config{}, fmt.Errorf("parsing %s: %w", path, err)
		}
	}

	if v := os.Getenv("QI_VAULT_PATH"); v != "" {
		raw.VaultPath = v
	}
	if v := os.Getenv("QI_TASK_FILE_PATH"); v != "" {
		raw.TaskFilePath = v
	}

	if raw.VaultPath == "" {
		return Config{}, errors.New("vault_path is required: set vault_path in config.toml or QI_VAULT_PATH env var")
	}

	taskFilePath := raw.TaskFilePath
	switch {
	case taskFilePath == "":
		taskFilePath = filepath.Join(raw.VaultPath, "10-tasks", "inbox.md")
	case !filepath.IsAbs(taskFilePath):
		taskFilePath = filepath.Join(raw.VaultPath, taskFilePath)
	}

	icsCalendars := make([]ICSCalendar, 0, len(raw.ICSCalendars))
	for _, c := range raw.ICSCalendars {
		if c.Name != "" && c.URL != "" {
			icsCalendars = append(icsCalendars, ICSCalendar{Name: c.Name, URL: c.URL})
		}
	}

	caldavCalendars := make([]CalDAVCalendar, 0, len(raw.CalDAVCalendars))
	for _, c := range raw.CalDAVCalendars {
		username := c.Username
		if username == "" {
			username = c.Email
		}
		endpoint := c.Endpoint
		if endpoint == "" && c.Email != "" {
			// Legacy Google CalDAV fallback for configs predating the generalized provider.
			endpoint = "https://apidata.googleusercontent.com/caldav/v2"
		}
		if c.Name == "" || username == "" || endpoint == "" {
			continue
		}
		caldavCalendars = append(caldavCalendars, CalDAVCalendar{
			Name:     c.Name,
			Endpoint: endpoint,
			Username: username,
			Password: c.Password, // may be empty; resolved at runtime via keychain
			Path:     c.Path,
		})
	}

	googleCalendars := make([]GoogleCalendar, 0, len(raw.GoogleCalendars))
	for _, c := range raw.GoogleCalendars {
		if c.Name == "" || c.Account == "" {
			continue
		}
		calID := c.CalendarID
		if calID == "" {
			calID = "primary"
		}
		googleCalendars = append(googleCalendars, GoogleCalendar{
			Name:       c.Name,
			Account:    c.Account,
			CalendarID: calID,
		})
	}

	mcpServers := make([]MCPServer, 0, len(raw.MCPServers))
	seenIDs := make(map[string]struct{}, len(raw.MCPServers))
	for _, s := range raw.MCPServers {
		if s.ID == "" || s.Command == "" {
			continue
		}
		if _, dup := seenIDs[s.ID]; dup {
			return Config{}, fmt.Errorf("mcp_servers: duplicate id %q", s.ID)
		}
		seenIDs[s.ID] = struct{}{}
		mcpServers = append(mcpServers, MCPServer{
			ID:      s.ID,
			Command: s.Command,
			Args:    s.Args,
			Env:     s.Env,
		})
	}

	return Config{
		VaultPath:       raw.VaultPath,
		TaskFilePath:    taskFilePath,
		InboxPath:       filepath.Join(raw.VaultPath, "00-inbox"),
		NotesPath:       filepath.Join(raw.VaultPath, "20-notes"),
		DailyPath:       filepath.Join(raw.VaultPath, "30-daily"),
		ICSCalendars:    icsCalendars,
		CalDAVCalendars: caldavCalendars,
		GoogleOAuth: GoogleOAuth{
			ClientID:     raw.GoogleOAuth.ClientID,
			ClientSecret: raw.GoogleOAuth.ClientSecret,
		},
		GoogleCalendars: googleCalendars,
		MCPServers:      mcpServers,
		AI: AIConfig{
			Provider:    raw.AI.Provider,
			Model:       raw.AI.Model,
			OllamaURL:   raw.AI.OllamaURL,
			OllamaModel: raw.AI.OllamaModel,
		},
	}, nil
}

func DataDir() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "qi")
}
