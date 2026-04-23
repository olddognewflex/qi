package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"qi/internal/config"
)

func writeTOML(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadFrom_TOMLBaseline(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("QI_TASK_FILE_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `
vault_path = "/tmp/vault"
task_file_path = "10-tasks/inbox.md"
`)

	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.VaultPath != "/tmp/vault" {
		t.Errorf("VaultPath = %q, want /tmp/vault", cfg.VaultPath)
	}
	want := "/tmp/vault/10-tasks/inbox.md"
	if cfg.TaskFilePath != want {
		t.Errorf("TaskFilePath = %q, want %q", cfg.TaskFilePath, want)
	}
}

func TestLoadFrom_EnvOverridesVaultPath(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `vault_path = "/tmp/toml-vault"`)
	t.Setenv("QI_VAULT_PATH", "/tmp/env-vault")

	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.VaultPath != "/tmp/env-vault" {
		t.Errorf("VaultPath = %q, want /tmp/env-vault", cfg.VaultPath)
	}
}

func TestLoadFrom_EnvOverridesTaskFilePath(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `vault_path = "/tmp/vault"`)
	t.Setenv("QI_TASK_FILE_PATH", "/tmp/env-tasks.md")

	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TaskFilePath != "/tmp/env-tasks.md" {
		t.Errorf("TaskFilePath = %q, want /tmp/env-tasks.md", cfg.TaskFilePath)
	}
}

func TestLoadFrom_MissingFileEnvFallback(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "/tmp/vault")

	cfg, err := config.LoadFrom("/nonexistent/config.toml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.VaultPath != "/tmp/vault" {
		t.Errorf("VaultPath = %q, want /tmp/vault", cfg.VaultPath)
	}
}

func TestLoadFrom_MissingVaultPathErrors(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("QI_TASK_FILE_PATH", "")

	_, err := config.LoadFrom("/nonexistent/config.toml")
	if err == nil {
		t.Fatal("expected error for missing vault_path, got nil")
	}
}

func TestLoadFrom_AbsoluteTaskFilePathUnchanged(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("QI_TASK_FILE_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `
vault_path = "/tmp/vault"
task_file_path = "/absolute/tasks.md"
`)

	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TaskFilePath != "/absolute/tasks.md" {
		t.Errorf("TaskFilePath = %q, want /absolute/tasks.md", cfg.TaskFilePath)
	}
}

func TestLoadFrom_DefaultTaskFilePath(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("QI_TASK_FILE_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `vault_path = "/tmp/vault"`)

	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "/tmp/vault/10-tasks/inbox.md"
	if cfg.TaskFilePath != want {
		t.Errorf("TaskFilePath = %q, want %q", cfg.TaskFilePath, want)
	}
}

func TestLoadFrom_MalformedTOML(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("QI_TASK_FILE_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `vault_path = [broken toml`)

	_, err := config.LoadFrom(cfgPath)
	if err == nil {
		t.Fatal("expected error for malformed TOML, got nil")
	}
}

func TestConfigPath_XDGEnvVar(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")

	got := config.ConfigPath()
	want := "/custom/xdg/qi/config.toml"
	if got != want {
		t.Errorf("ConfigPath() = %q, want %q", got, want)
	}
}

func TestConfigPath_DefaultFallback(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	got := config.ConfigPath()
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".config", "qi", "config.toml")
	if got != want {
		t.Errorf("ConfigPath() = %q, want %q", got, want)
	}
}
