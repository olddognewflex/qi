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
	Email    string
	Password string
}

type Config struct {
	VaultPath       string
	TaskFilePath    string
	InboxPath       string
	NotesPath       string
	DailyPath       string
	ICSCalendars    []ICSCalendar
	CalDAVCalendars []CalDAVCalendar
}

type icsCalTOML struct {
	Name string `toml:"name"`
	URL  string `toml:"url"`
}

type caldavCalTOML struct {
	Name     string `toml:"name"`
	Email    string `toml:"email"`
	Password string `toml:"password"`
}

type tomlFile struct {
	VaultPath       string          `toml:"vault_path"`
	TaskFilePath    string          `toml:"task_file_path"`
	ICSCalendars    []icsCalTOML    `toml:"ics_calendars"`
	CalDAVCalendars []caldavCalTOML `toml:"caldav_calendars"`
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
		if c.Name != "" && c.Email != "" && c.Password != "" {
			caldavCalendars = append(caldavCalendars, CalDAVCalendar{Name: c.Name, Email: c.Email, Password: c.Password})
		}
	}

	return Config{
		VaultPath:       raw.VaultPath,
		TaskFilePath:    taskFilePath,
		InboxPath:       filepath.Join(raw.VaultPath, "00-inbox"),
		NotesPath:       filepath.Join(raw.VaultPath, "20-notes"),
		DailyPath:       filepath.Join(raw.VaultPath, "30-daily"),
		ICSCalendars:    icsCalendars,
		CalDAVCalendars: caldavCalendars,
	}, nil
}
