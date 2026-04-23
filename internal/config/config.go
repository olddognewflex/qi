package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	VaultPath    string
	TaskFilePath string
}

type tomlFile struct {
	VaultPath    string `toml:"vault_path"`
	TaskFilePath string `toml:"task_file_path"`
}

// ConfigPath returns the XDG-compliant path: $XDG_CONFIG_HOME/qi/config.toml,
// falling back to ~/.config/qi/config.toml.
func ConfigPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "qi", "config.toml")
}

func Load() (Config, error) {
	return LoadFrom(ConfigPath())
}

// LoadFrom reads TOML from path then applies env var overrides.
// Missing file is not an error. Priority: env vars > TOML > built-in defaults.
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

	return Config{
		VaultPath:    raw.VaultPath,
		TaskFilePath: taskFilePath,
	}, nil
}
