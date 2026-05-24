package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

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

func TestLoadFrom_MCPServers(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("QI_TASK_FILE_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `
vault_path = "/tmp/vault"

[[mcp_servers]]
id = "github"
command = "/usr/local/bin/mcp-github"
args = ["--mode", "stdio"]
env = { GITHUB_TOKEN = "abc123" }

[[mcp_servers]]
id = "obsidian"
command = "/usr/local/bin/mcp-obsidian"
`)

	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.MCPServers) != 2 {
		t.Fatalf("got %d servers, want 2", len(cfg.MCPServers))
	}
	gh := cfg.MCPServers[0]
	if gh.ID != "github" || gh.Command != "/usr/local/bin/mcp-github" {
		t.Errorf("github server = %+v", gh)
	}
	if len(gh.Args) != 2 || gh.Args[0] != "--mode" {
		t.Errorf("github args = %v", gh.Args)
	}
	if gh.Env["GITHUB_TOKEN"] != "abc123" {
		t.Errorf("github env = %v", gh.Env)
	}
}

func TestLoadFrom_MCPServersRejectDuplicateID(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("QI_TASK_FILE_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `
vault_path = "/tmp/vault"

[[mcp_servers]]
id = "dup"
command = "/bin/true"

[[mcp_servers]]
id = "dup"
command = "/bin/false"
`)
	if _, err := config.LoadFrom(cfgPath); err == nil {
		t.Fatal("expected duplicate-id error")
	}
}

func TestLoadFrom_MCPServersSkipIncomplete(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("QI_TASK_FILE_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `
vault_path = "/tmp/vault"

[[mcp_servers]]
id = ""
command = "/bin/true"

[[mcp_servers]]
id = "ok"
command = ""

[[mcp_servers]]
id = "good"
command = "/bin/true"
`)
	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.MCPServers) != 1 || cfg.MCPServers[0].ID != "good" {
		t.Fatalf("got %+v, want only \"good\"", cfg.MCPServers)
	}
}

func TestLoadFrom_AISection(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("QI_TASK_FILE_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `
vault_path = "/tmp/vault"

[ai]
provider = "ollama"
model = "claude-sonnet-4-6"
ollama_url = "http://localhost:11434"
ollama_model = "qwen3:14b"
`)
	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AI.Provider != "ollama" {
		t.Errorf("provider = %q", cfg.AI.Provider)
	}
	if cfg.AI.OllamaModel != "qwen3:14b" {
		t.Errorf("ollama_model = %q", cfg.AI.OllamaModel)
	}
	if cfg.AI.Model != "claude-sonnet-4-6" {
		t.Errorf("model = %q", cfg.AI.Model)
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

func TestDailyNotePath_NestedFormat(t *testing.T) {
	cfg := config.Config{
		VaultPath:       "/tmp/vault",
		DailyDirFormat:  "30-daily/YYYY/MMMM",
		DailyFileFormat: "YYYY-MM-DD",
	}
	day := time.Date(2026, 5, 24, 0, 0, 0, 0, time.Local)
	got := cfg.DailyNotePath(day)
	want := "/tmp/vault/30-daily/2026/May/2026-05-24.md"
	if got != want {
		t.Errorf("DailyNotePath = %q, want %q", got, want)
	}
}

func TestDailyNotePath_FlatDefault(t *testing.T) {
	cfg := config.Config{
		VaultPath:       "/tmp/vault",
		DailyDirFormat:  "30-daily",
		DailyFileFormat: "YYYY-MM-DD",
	}
	day := time.Date(2026, 5, 24, 0, 0, 0, 0, time.Local)
	got := cfg.DailyNotePath(day)
	want := "/tmp/vault/30-daily/2026-05-24.md"
	if got != want {
		t.Errorf("DailyNotePath = %q, want %q", got, want)
	}
}

func TestDailyNotePath_ShortMonthToken(t *testing.T) {
	// MMM (short month) must resolve independently of MMMM/MM.
	cfg := config.Config{
		VaultPath:       "/v",
		DailyDirFormat:  "daily/MMM",
		DailyFileFormat: "MM-DD",
	}
	day := time.Date(2026, 1, 5, 0, 0, 0, 0, time.Local)
	got := cfg.DailyNotePath(day)
	want := "/v/daily/Jan/01-05.md"
	if got != want {
		t.Errorf("DailyNotePath = %q, want %q", got, want)
	}
}

func TestLoadFrom_DailyFormatsFromTOML(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("QI_TASK_FILE_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `
vault_path = "/tmp/vault"
daily_dir_format = "30-daily/YYYY/MMMM"
daily_file_format = "YYYY-MM-DD"
`)

	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 5, 24, 0, 0, 0, 0, time.Local)
	got := cfg.DailyNotePath(day)
	want := "/tmp/vault/30-daily/2026/May/2026-05-24.md"
	if got != want {
		t.Errorf("DailyNotePath = %q, want %q", got, want)
	}
}

func TestLoadFrom_DailyFormatDefaults(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("QI_TASK_FILE_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `vault_path = "/tmp/vault"`)

	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DailyDirFormat != "30-daily" {
		t.Errorf("DailyDirFormat = %q, want 30-daily", cfg.DailyDirFormat)
	}
	if cfg.DailyFileFormat != "YYYY-MM-DD" {
		t.Errorf("DailyFileFormat = %q, want YYYY-MM-DD", cfg.DailyFileFormat)
	}
	day := time.Date(2026, 5, 24, 0, 0, 0, 0, time.Local)
	want := "/tmp/vault/30-daily/2026-05-24.md"
	if got := cfg.DailyNotePath(day); got != want {
		t.Errorf("DailyNotePath = %q, want %q", got, want)
	}
}
