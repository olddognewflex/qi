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

func TestLoadFrom_ProjectExplicitFile(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("QI_TASK_FILE_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `
vault_path = "/tmp/vault"

[[project]]
project = "foo"
vault_path = "/Users/you/Vaults/foo"
file = "10-tasks/foo.md"
`)

	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != 1 {
		t.Fatalf("got %d project vaults, want 1", len(cfg.Projects))
	}
	pv := cfg.Projects[0]
	if pv.Project != "foo" {
		t.Errorf("Project = %q, want foo", pv.Project)
	}
	if pv.VaultPath != "/Users/you/Vaults/foo" {
		t.Errorf("Path = %q", pv.VaultPath)
	}
	if pv.File != "/Users/you/Vaults/foo/10-tasks/foo.md" {
		t.Errorf("File = %q, want /Users/you/Vaults/foo/10-tasks/foo.md", pv.File)
	}
}

func TestLoadFrom_ProjectDefaultFile(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("QI_TASK_FILE_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `
vault_path = "/tmp/vault"

[[project]]
project = "foo"
vault_path = "/Users/you/Vaults/foo"

[[project]]
project = "work/clientA"
vault_path = "/Users/you/Vaults/work"
`)

	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != 2 {
		t.Fatalf("got %d project vaults, want 2", len(cfg.Projects))
	}
	// Simple project: default file = 10-tasks/foo.md
	simple := cfg.Projects[0]
	if simple.File != "/Users/you/Vaults/foo/10-tasks/foo.md" {
		t.Errorf("simple File = %q, want /Users/you/Vaults/foo/10-tasks/foo.md", simple.File)
	}
	// Nested tag: / flattened to - in filename; project preserved verbatim
	nested := cfg.Projects[1]
	if nested.Project != "work/clientA" {
		t.Errorf("nested Project = %q, want work/clientA", nested.Project)
	}
	if nested.File != "/Users/you/Vaults/work/10-tasks/work-clientA.md" {
		t.Errorf("nested File = %q, want /Users/you/Vaults/work/10-tasks/work-clientA.md", nested.File)
	}
}

func TestLoadFrom_ProjectRelativeFileResolved(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("QI_TASK_FILE_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `
vault_path = "/tmp/vault"

[[project]]
project = "bar"
vault_path = "/Users/you/Vaults/bar"
file = "tasks/bar-tasks.md"
`)

	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	pv := cfg.Projects[0]
	want := "/Users/you/Vaults/bar/tasks/bar-tasks.md"
	if pv.File != want {
		t.Errorf("File = %q, want %q", pv.File, want)
	}
}

func TestLoadFrom_ProjectDuplicateProjectError(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("QI_TASK_FILE_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `
vault_path = "/tmp/vault"

[[project]]
project = "foo"
vault_path = "/Users/you/Vaults/foo"

[[project]]
project = "foo"
vault_path = "/Users/you/Vaults/foo2"
`)

	if _, err := config.LoadFrom(cfgPath); err == nil {
		t.Fatal("expected duplicate project error, got nil")
	}
}

func TestLoadFrom_ProjectDuplicateFileError(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("QI_TASK_FILE_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `
vault_path = "/tmp/vault"

[[project]]
project = "alpha"
vault_path = "/Users/you/Vaults/shared"
file = "10-tasks/tasks.md"

[[project]]
project = "beta"
vault_path = "/Users/you/Vaults/shared"
file = "10-tasks/tasks.md"
`)

	if _, err := config.LoadFrom(cfgPath); err == nil {
		t.Fatal("expected duplicate resolved file error, got nil")
	}
}

func TestLoadFrom_ProjectMissingProject(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("QI_TASK_FILE_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `
vault_path = "/tmp/vault"

[[project]]
project = ""
vault_path = "/Users/you/Vaults/foo"
`)

	if _, err := config.LoadFrom(cfgPath); err == nil {
		t.Fatal("expected error for missing project, got nil")
	}
}

func TestLoadFrom_ProjectMissingPath(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("QI_TASK_FILE_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `
vault_path = "/tmp/vault"

[[project]]
project = "foo"
vault_path = ""
`)

	if _, err := config.LoadFrom(cfgPath); err == nil {
		t.Fatal("expected error for missing path, got nil")
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

func TestResolveLaunch_PerProjectOverridesGlobal(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("AI_HARNESS", "")
	t.Setenv("AI_EDITOR", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `
vault_path = "/tmp/vault"

[launch]
harness = "claude"
args = ["--global"]

[[project]]
project = "acme"
vault_path = "/tmp/acme"
  [project.launch]
  harness = "aider"
  args = ["--model", "sonnet"]
  detach = false

[[project]]
project = "beta"
vault_path = "/tmp/beta"
`)

	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	// Per-project override wins.
	lc, err := cfg.ResolveLaunch("acme")
	if err != nil {
		t.Fatal(err)
	}
	if lc.Harness != "aider" || len(lc.Args) != 2 || lc.Args[0] != "--model" {
		t.Errorf("acme launch = %+v, want aider --model sonnet", lc)
	}

	// Project without its own launch falls back to global.
	lc, err = cfg.ResolveLaunch("beta")
	if err != nil {
		t.Fatal(err)
	}
	if lc.Harness != "claude" || len(lc.Args) != 1 || lc.Args[0] != "--global" {
		t.Errorf("beta launch = %+v, want global claude", lc)
	}

	// No project selects global.
	lc, err = cfg.ResolveLaunch("")
	if err != nil {
		t.Fatal(err)
	}
	if lc.Harness != "claude" {
		t.Errorf("default launch = %+v, want global claude", lc)
	}
}

func TestResolveLaunch_EnvFallback(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `vault_path = "/tmp/vault"`)

	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("AI_EDITOR", "")
	t.Setenv("AI_HARNESS", "cursor")
	lc, err := cfg.ResolveLaunch("")
	if err != nil {
		t.Fatal(err)
	}
	if lc.Harness != "cursor" {
		t.Errorf("env fallback = %q, want cursor", lc.Harness)
	}

	// AI_HARNESS takes precedence over AI_EDITOR.
	t.Setenv("AI_EDITOR", "code")
	lc, _ = cfg.ResolveLaunch("")
	if lc.Harness != "cursor" {
		t.Errorf("precedence = %q, want cursor (AI_HARNESS over AI_EDITOR)", lc.Harness)
	}

	// AI_EDITOR used when AI_HARNESS empty.
	t.Setenv("AI_HARNESS", "")
	lc, _ = cfg.ResolveLaunch("")
	if lc.Harness != "code" {
		t.Errorf("AI_EDITOR fallback = %q, want code", lc.Harness)
	}
}

func TestResolveLaunch_NoneConfigured(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("AI_HARNESS", "")
	t.Setenv("AI_EDITOR", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `vault_path = "/tmp/vault"`)

	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.ResolveLaunch(""); err == nil {
		t.Error("expected error when no harness configured, got nil")
	}
}

func TestResolveLaunch_UnknownProject(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("AI_HARNESS", "")
	t.Setenv("AI_EDITOR", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `
vault_path = "/tmp/vault"
[launch]
harness = "claude"
`)

	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.ResolveLaunch("nonexistent"); err == nil {
		t.Error("expected error for unknown project, got nil")
	}
}

func TestEffectiveProject(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `
vault_path = "/tmp/vault"

[[project]]
project = "acme"
vault_path = "/tmp/acme"
`)
	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	// Explicit flag wins verbatim, even if unmatched (typo surfaces later).
	t.Setenv("WORK_CONTEXT", "acme")
	if got := cfg.EffectiveProject("ghost"); got != "ghost" {
		t.Errorf("flag override = %q, want ghost", got)
	}

	// Empty flag + matching WORK_CONTEXT → that project.
	if got := cfg.EffectiveProject(""); got != "acme" {
		t.Errorf("env match = %q, want acme", got)
	}

	// Empty flag + WORK_CONTEXT with no matching vault → "" (lenient).
	t.Setenv("WORK_CONTEXT", "unmapped-client")
	if got := cfg.EffectiveProject(""); got != "" {
		t.Errorf("unmatched env = %q, want empty", got)
	}

	// No flag, no env → "".
	t.Setenv("WORK_CONTEXT", "")
	if got := cfg.EffectiveProject(""); got != "" {
		t.Errorf("no selection = %q, want empty", got)
	}
}
