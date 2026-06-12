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

func TestLoadFrom_SyncSection(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("QI_TASK_FILE_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `
vault_path = "/tmp/vault"

[sync]
watch = true
debounce_ms = 1200
`)
	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Sync.Watch {
		t.Errorf("Sync.Watch = %v, want true", cfg.Sync.Watch)
	}
	if cfg.Sync.DebounceMS != 1200 {
		t.Errorf("Sync.DebounceMS = %d, want 1200", cfg.Sync.DebounceMS)
	}
}

func TestLoadFrom_SyncDefaults(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("QI_TASK_FILE_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `vault_path = "/tmp/vault"`)

	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	// No [sync] block -> watch off, debounce raw (default applied by the watcher).
	if cfg.Sync.Watch {
		t.Errorf("Sync.Watch = %v, want false by default", cfg.Sync.Watch)
	}
	if cfg.Sync.DebounceMS != 0 {
		t.Errorf("Sync.DebounceMS = %d, want 0 (default applied in watcher)", cfg.Sync.DebounceMS)
	}
}

func TestLoadFrom_NotifySection(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("QI_TASK_FILE_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `
vault_path = "/tmp/vault"

[notify]
due_today = true
at = "07:30"
`)
	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Notify.DueToday {
		t.Errorf("Notify.DueToday = %v, want true", cfg.Notify.DueToday)
	}
	if cfg.Notify.At != "07:30" {
		t.Errorf("Notify.At = %q, want 07:30", cfg.Notify.At)
	}
}

func TestLoadFrom_NotifyDefaults(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("QI_TASK_FILE_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `vault_path = "/tmp/vault"`)

	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	// No [notify] block -> due-today off, At empty (default applied by the scheduler).
	if cfg.Notify.DueToday {
		t.Errorf("Notify.DueToday = %v, want false by default", cfg.Notify.DueToday)
	}
	if cfg.Notify.At != "" {
		t.Errorf("Notify.At = %q, want \"\" (default applied in scheduler)", cfg.Notify.At)
	}
}

func TestLoadFrom_EmbeddingsSection(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("QI_TASK_FILE_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `
vault_path = "/tmp/vault"

[embeddings]
enabled = true
model = "nomic-embed-text"
ollama_url = "http://localhost:11434"
`)
	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Embeddings.Enabled {
		t.Errorf("Embeddings.Enabled = %v, want true", cfg.Embeddings.Enabled)
	}
	if cfg.Embeddings.Model != "nomic-embed-text" {
		t.Errorf("Embeddings.Model = %q, want nomic-embed-text", cfg.Embeddings.Model)
	}
	if cfg.Embeddings.OllamaURL != "http://localhost:11434" {
		t.Errorf("Embeddings.OllamaURL = %q", cfg.Embeddings.OllamaURL)
	}
}

func TestLoadFrom_EmbeddingsDefaults(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("QI_TASK_FILE_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `vault_path = "/tmp/vault"`)

	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	// No [embeddings] block -> disabled, model/url empty (defaults applied by consumers).
	if cfg.Embeddings.Enabled {
		t.Errorf("Embeddings.Enabled = %v, want false by default", cfg.Embeddings.Enabled)
	}
	if cfg.Embeddings.Model != "" {
		t.Errorf("Embeddings.Model = %q, want \"\" (default applied in embed)", cfg.Embeddings.Model)
	}
	if cfg.Embeddings.OllamaURL != "" {
		t.Errorf("Embeddings.OllamaURL = %q, want \"\" (default applied in embed)", cfg.Embeddings.OllamaURL)
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

func TestLoadFrom_ClientProjectFiles(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("QI_TASK_FILE_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `
vault_path = "/tmp/vault"

[[client]]
name = "Consular Capital"
vault_path = "/Users/you/Vaults/CC"
dev_root = "/Users/you/Development/CC"

  [[client.project]]
  project = "CC"

  [[client.project]]
  project = "BHQ"
  dev_path = "builder-hq"
  task_file = "10-projects/BuilderHQ/tasks.md"

  [[client.project]]
  project = "work/clientA"
`)

	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Clients) != 1 {
		t.Fatalf("got %d clients, want 1", len(cfg.Clients))
	}
	if len(cfg.Projects) != 3 {
		t.Fatalf("got %d projects, want 3", len(cfg.Projects))
	}

	// CC: inherits client vault, default file under it, no dev_path.
	cc, ok := cfg.ProjectByName("CC")
	if !ok {
		t.Fatal("CC not found")
	}
	if cc.Client != "Consular Capital" {
		t.Errorf("CC.Client = %q", cc.Client)
	}
	if cc.VaultPath != "/Users/you/Vaults/CC" {
		t.Errorf("CC.VaultPath = %q (want client vault)", cc.VaultPath)
	}
	if cc.File != "/Users/you/Vaults/CC/10-tasks/CC.md" {
		t.Errorf("CC.File = %q", cc.File)
	}
	if cc.DevPath != "" {
		t.Errorf("CC.DevPath = %q, want empty", cc.DevPath)
	}

	// BHQ: relative dev_path resolves under client dev_root; explicit file under vault.
	bhq, _ := cfg.ProjectByName("BHQ")
	if bhq.DevPath != "/Users/you/Development/CC/builder-hq" {
		t.Errorf("BHQ.DevPath = %q (want resolved under dev_root)", bhq.DevPath)
	}
	if bhq.File != "/Users/you/Vaults/CC/10-projects/BuilderHQ/tasks.md" {
		t.Errorf("BHQ.File = %q", bhq.File)
	}

	// Nested tag: "/" flattened to "-" in default filename.
	wc, _ := cfg.ProjectByName("work/clientA")
	if wc.File != "/Users/you/Vaults/CC/10-tasks/work-clientA.md" {
		t.Errorf("work/clientA File = %q", wc.File)
	}
}

func TestLoadFrom_ProjectVaultOverride(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("QI_TASK_FILE_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `
vault_path = "/tmp/vault"

[[client]]
name = "acme"
vault_path = "/Users/you/Vaults/acme"

  [[client.project]]
  project = "special"
  vault_path = "/Users/you/Vaults/special"
`)
	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := cfg.ProjectByName("special")
	if p.VaultPath != "/Users/you/Vaults/special" {
		t.Errorf("VaultPath = %q, want project override", p.VaultPath)
	}
	if p.File != "/Users/you/Vaults/special/10-tasks/special.md" {
		t.Errorf("File = %q (want under override vault)", p.File)
	}
}

func TestLoadFrom_ClientValidation(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("QI_TASK_FILE_PATH", "")
	cases := map[string]string{
		"missing client name": `
vault_path = "/tmp/vault"
[[client]]
vault_path = "/v"
`,
		"missing client vault_path": `
vault_path = "/tmp/vault"
[[client]]
name = "c"
`,
		"missing project tag": `
vault_path = "/tmp/vault"
[[client]]
name = "c"
vault_path = "/v"
  [[client.project]]
  project = ""
`,
		"duplicate client name": `
vault_path = "/tmp/vault"
[[client]]
name = "c"
vault_path = "/v"
[[client]]
name = "c"
vault_path = "/v2"
`,
		"duplicate project across clients": `
vault_path = "/tmp/vault"
[[client]]
name = "c1"
vault_path = "/v1"
  [[client.project]]
  project = "dup"
[[client]]
name = "c2"
vault_path = "/v2"
  [[client.project]]
  project = "dup"
`,
		"duplicate resolved file": `
vault_path = "/tmp/vault"
[[client]]
name = "c"
vault_path = "/shared"
  [[client.project]]
  project = "a"
  task_file = "10-tasks/t.md"
  [[client.project]]
  project = "b"
  task_file = "10-tasks/t.md"
`,
		"relative dev_path without dev_root": `
vault_path = "/tmp/vault"
[[client]]
name = "c"
vault_path = "/v"
  [[client.project]]
  project = "p"
  dev_path = "sub"
`,
	}
	for name, toml := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			cfgPath := filepath.Join(dir, "config.toml")
			writeTOML(t, cfgPath, toml)
			if _, err := config.LoadFrom(cfgPath); err == nil {
				t.Errorf("%s: expected error, got nil", name)
			}
		})
	}
}

func TestLoadFrom_DevPathAbsolute(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `
vault_path = "/tmp/vault"
[[client]]
name = "c"
vault_path = "/v"
dev_root = "/dev/c"
  [[client.project]]
  project = "abs"
  dev_path = "/elsewhere/abs"
`)
	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := cfg.ProjectByName("abs")
	if p.DevPath != "/elsewhere/abs" {
		t.Errorf("DevPath = %q, want absolute kept as-is", p.DevPath)
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

// launchTestConfig builds a config exercising all four harness tiers.
func launchTestConfig(t *testing.T) config.Config {
	t.Helper()
	t.Setenv("QI_VAULT_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `
vault_path = "/tmp/mainvault"

[launch]
harness = "claude"
args = ["--global"]

[[client]]
name = "CC"
vault_path = "/vaults/cc"
dev_root = "/dev/cc"
  [client.launch]
  harness = "agent"

  [[client.project]]
  project = "BHQ"
  dev_path = "builder-hq"

  [[client.project]]
  project = "OVERRIDE"
  dev_path = "ovr"
    [client.project.launch]
    harness = "aider"

[[client]]
name = "Solo"
vault_path = "/vaults/solo"
dev_root = "/dev/solo"

  [[client.project]]
  project = "SOLO1"
`)
	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestResolveLaunchTarget_ProjectInheritsClient(t *testing.T) {
	t.Setenv("AI_HARNESS", "")
	t.Setenv("AI_EDITOR", "")
	t.Setenv("WORK_CONTEXT", "")
	cfg := launchTestConfig(t)

	// BHQ: no project launch -> inherits client "agent"; cwd = project dev_path.
	tgt, err := cfg.ResolveLaunchTarget("BHQ")
	if err != nil {
		t.Fatal(err)
	}
	if tgt.Harness.Harness != "agent" {
		t.Errorf("BHQ harness = %q, want agent (client default)", tgt.Harness.Harness)
	}
	if tgt.VaultPath != "/vaults/cc" {
		t.Errorf("BHQ vault = %q, want client vault", tgt.VaultPath)
	}
	if tgt.WorkDir != "/dev/cc/builder-hq" {
		t.Errorf("BHQ workdir = %q, want resolved dev_path", tgt.WorkDir)
	}
}

func TestResolveLaunchTarget_ProjectOverridesClient(t *testing.T) {
	t.Setenv("AI_HARNESS", "")
	t.Setenv("AI_EDITOR", "")
	t.Setenv("WORK_CONTEXT", "")
	cfg := launchTestConfig(t)

	tgt, err := cfg.ResolveLaunchTarget("OVERRIDE")
	if err != nil {
		t.Fatal(err)
	}
	if tgt.Harness.Harness != "aider" {
		t.Errorf("OVERRIDE harness = %q, want aider (project override)", tgt.Harness.Harness)
	}
}

func TestResolveLaunchTarget_ClientMatchUsesDevRoot(t *testing.T) {
	t.Setenv("AI_HARNESS", "")
	t.Setenv("AI_EDITOR", "")
	t.Setenv("WORK_CONTEXT", "")
	cfg := launchTestConfig(t)

	// Client name (not a project) -> client harness + dev_root as cwd.
	tgt, err := cfg.ResolveLaunchTarget("CC")
	if err != nil {
		t.Fatal(err)
	}
	if tgt.Harness.Harness != "agent" {
		t.Errorf("CC harness = %q, want agent", tgt.Harness.Harness)
	}
	if tgt.VaultPath != "/vaults/cc" {
		t.Errorf("CC vault = %q", tgt.VaultPath)
	}
	if tgt.WorkDir != "/dev/cc" {
		t.Errorf("CC workdir = %q, want client dev_root", tgt.WorkDir)
	}
}

func TestResolveLaunchTarget_ClientFallsToGlobalHarness(t *testing.T) {
	t.Setenv("AI_HARNESS", "")
	t.Setenv("AI_EDITOR", "")
	t.Setenv("WORK_CONTEXT", "")
	cfg := launchTestConfig(t)

	// Solo client has no [client.launch] -> global "claude".
	tgt, err := cfg.ResolveLaunchTarget("Solo")
	if err != nil {
		t.Fatal(err)
	}
	if tgt.Harness.Harness != "claude" {
		t.Errorf("Solo harness = %q, want global claude", tgt.Harness.Harness)
	}
	if tgt.WorkDir != "/dev/solo" {
		t.Errorf("Solo workdir = %q, want /dev/solo", tgt.WorkDir)
	}
}

func TestResolveLaunchTarget_NoTargetUsesGlobal(t *testing.T) {
	t.Setenv("AI_HARNESS", "")
	t.Setenv("AI_EDITOR", "")
	t.Setenv("WORK_CONTEXT", "")
	cfg := launchTestConfig(t)

	tgt, err := cfg.ResolveLaunchTarget("")
	if err != nil {
		t.Fatal(err)
	}
	if tgt.Harness.Harness != "claude" || len(tgt.Harness.Args) != 1 {
		t.Errorf("no-target harness = %+v, want global claude --global", tgt.Harness)
	}
	if tgt.VaultPath != "/tmp/mainvault" {
		t.Errorf("no-target vault = %q, want global vault", tgt.VaultPath)
	}
	if tgt.WorkDir != "" {
		t.Errorf("no-target workdir = %q, want empty (current dir)", tgt.WorkDir)
	}
}

func TestResolveLaunchTarget_WorkContext(t *testing.T) {
	t.Setenv("AI_HARNESS", "")
	t.Setenv("AI_EDITOR", "")
	cfg := launchTestConfig(t)

	// $WORK_CONTEXT matches a project -> FromEnv true, project resolution.
	t.Setenv("WORK_CONTEXT", "BHQ")
	tgt, err := cfg.ResolveLaunchTarget("")
	if err != nil {
		t.Fatal(err)
	}
	if !tgt.FromEnv || tgt.WorkDir != "/dev/cc/builder-hq" {
		t.Errorf("WORK_CONTEXT=BHQ -> %+v, want FromEnv project resolution", tgt)
	}

	// Explicit flag beats $WORK_CONTEXT and is not marked FromEnv.
	tgt, _ = cfg.ResolveLaunchTarget("CC")
	if tgt.FromEnv || tgt.WorkDir != "/dev/cc" {
		t.Errorf("flag CC over env BHQ -> %+v, want client CC, not FromEnv", tgt)
	}

	// Unmatched $WORK_CONTEXT is lenient -> global default, no error.
	t.Setenv("WORK_CONTEXT", "unmapped")
	tgt, err = cfg.ResolveLaunchTarget("")
	if err != nil {
		t.Fatalf("unmatched WORK_CONTEXT should be lenient, got %v", err)
	}
	if tgt.Harness.Harness != "claude" || tgt.Label != "" {
		t.Errorf("unmatched env -> %+v, want silent global", tgt)
	}
}

func TestResolveLaunchTarget_UnknownFlagErrors(t *testing.T) {
	t.Setenv("AI_HARNESS", "")
	t.Setenv("AI_EDITOR", "")
	t.Setenv("WORK_CONTEXT", "")
	cfg := launchTestConfig(t)

	if _, err := cfg.ResolveLaunchTarget("ghost"); err == nil {
		t.Error("explicit unknown target should error")
	}
}

func TestResolveLaunchTarget_EnvHarnessFallback(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("WORK_CONTEXT", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `vault_path = "/tmp/vault"`)
	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	// No [launch], no client -> $AI_HARNESS, then $AI_EDITOR.
	t.Setenv("AI_EDITOR", "code")
	t.Setenv("AI_HARNESS", "cursor")
	tgt, err := cfg.ResolveLaunchTarget("")
	if err != nil {
		t.Fatal(err)
	}
	if tgt.Harness.Harness != "cursor" {
		t.Errorf("harness = %q, want cursor ($AI_HARNESS over $AI_EDITOR)", tgt.Harness.Harness)
	}

	t.Setenv("AI_HARNESS", "")
	tgt, _ = cfg.ResolveLaunchTarget("")
	if tgt.Harness.Harness != "code" {
		t.Errorf("harness = %q, want code ($AI_EDITOR)", tgt.Harness.Harness)
	}

	// Nothing configured anywhere -> error.
	t.Setenv("AI_EDITOR", "")
	if _, err := cfg.ResolveLaunchTarget(""); err == nil {
		t.Error("expected error when no harness configured anywhere")
	}
}

func TestProjectAndClientByName(t *testing.T) {
	t.Setenv("AI_HARNESS", "")
	t.Setenv("AI_EDITOR", "")
	t.Setenv("WORK_CONTEXT", "")
	cfg := launchTestConfig(t)

	if _, ok := cfg.ProjectByName("BHQ"); !ok {
		t.Error("ProjectByName(BHQ) = false")
	}
	if _, ok := cfg.ProjectByName(""); ok {
		t.Error("ProjectByName(\"\") = true")
	}
	if _, ok := cfg.ClientByName("CC"); !ok {
		t.Error("ClientByName(CC) = false")
	}
	if _, ok := cfg.ClientByName("ghost"); ok {
		t.Error("ClientByName(ghost) = true")
	}
}

func TestLoadFrom_ClientTaskFile(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("AI_HARNESS", "")
	t.Setenv("AI_EDITOR", "")
	t.Setenv("WORK_CONTEXT", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `
vault_path = "/tmp/vault"

[[client]]
name = "CC"
vault_path = "/vaults/cc"
dev_root = "/dev/cc"
task_file = "10-tasks/_client.md"
  [client.launch]
  harness = "agent"

  [[client.project]]
  project = "BHQ"
  dev_path = "builder-hq"
`)
	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	// Client retains its resolved task_file.
	cl, ok := cfg.ClientByName("CC")
	if !ok {
		t.Fatal("CC client not found")
	}
	if cl.TaskFile != "/vaults/cc/10-tasks/_client.md" {
		t.Errorf("CC.TaskFile = %q", cl.TaskFile)
	}

	// A synthetic project tagged by the client name is flattened in for sync.
	var synth *config.ProjectConfig
	for i := range cfg.Projects {
		if cfg.Projects[i].Project == "CC" {
			synth = &cfg.Projects[i]
		}
	}
	if synth == nil {
		t.Fatal("no synthetic CC project in Projects")
	}
	if synth.File != "/vaults/cc/10-tasks/_client.md" || synth.VaultPath != "/vaults/cc" {
		t.Errorf("synthetic CC = %+v", *synth)
	}

	// ProjectByName must NOT surface the synthetic project (so it can't shadow
	// client-name launch resolution).
	if _, ok := cfg.ProjectByName("CC"); ok {
		t.Error("ProjectByName(CC) returned the synthetic projection; want skipped")
	}

	// Launch by client name still resolves to the client (dev_root), not the
	// synthetic project (which would give current dir).
	tgt, err := cfg.ResolveLaunchTarget("CC")
	if err != nil {
		t.Fatal(err)
	}
	if tgt.WorkDir != "/dev/cc" {
		t.Errorf("ResolveLaunchTarget(CC).WorkDir = %q, want client dev_root", tgt.WorkDir)
	}
}

func TestLoadFrom_ClientTaskFileTagCollision(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	// Client task_file tag "CC" collides with a project also named "CC".
	writeTOML(t, cfgPath, `
vault_path = "/tmp/vault"
[[client]]
name = "CC"
vault_path = "/vaults/cc"
task_file = "10-tasks/_client.md"
  [[client.project]]
  project = "CC"
`)
	if _, err := config.LoadFrom(cfgPath); err == nil {
		t.Error("expected tag-collision error, got nil")
	}
}

func TestNoteVaultFor(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("AI_HARNESS", "")
	t.Setenv("AI_EDITOR", "")
	t.Setenv("WORK_CONTEXT", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `
vault_path = "/tmp/main"
[[client]]
name = "CC"
vault_path = "/vaults/cc"
dev_root = "/dev/cc"
  [[client.project]]
  project = "BHQ"
  dev_path = "builder-hq"
[[client]]
name = "DD"
vault_path = "/vaults/dd"
notes_path = "notes"
  [[client.project]]
  project = "ZZ"
  notes_path = "10-zz"
  [[client.project]]
  project = "YY"
`)
	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	if v, d, _ := cfg.NoteVaultFor("", ""); v != "/tmp/main" || d != "/tmp/main/20-notes" {
		t.Errorf("default note vault = %q, dir = %q, want main + 20-notes", v, d)
	}
	if v, d, _ := cfg.NoteVaultFor("CC", ""); v != "/vaults/cc" || d != "/vaults/cc/00-inbox" {
		t.Errorf("client note vault = %q, dir = %q, want /vaults/cc + default 00-inbox", v, d)
	}
	if v, d, _ := cfg.NoteVaultFor("", "BHQ"); v != "/vaults/cc" || d != "/vaults/cc/00-inbox" {
		t.Errorf("project note vault = %q, dir = %q, want project vault + default 00-inbox", v, d)
	}
	if _, d, _ := cfg.NoteVaultFor("DD", ""); d != "/vaults/dd/notes" {
		t.Errorf("client notes_path dir = %q, want /vaults/dd/notes", d)
	}
	if _, d, _ := cfg.NoteVaultFor("", "ZZ"); d != "/vaults/dd/10-zz" {
		t.Errorf("project notes_path dir = %q, want /vaults/dd/10-zz", d)
	}
	if _, d, _ := cfg.NoteVaultFor("", "YY"); d != "/vaults/dd/notes" {
		t.Errorf("project should inherit client notes_path, got %q, want /vaults/dd/notes", d)
	}
	if _, _, err := cfg.NoteVaultFor("CC", "BHQ"); err == nil {
		t.Error("both client+project should error")
	}
	if _, _, err := cfg.NoteVaultFor("ghost", ""); err == nil {
		t.Error("unknown client should error")
	}
	if _, _, err := cfg.NoteVaultFor("", "ghost"); err == nil {
		t.Error("unknown project should error")
	}
}

func TestProjectPath(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("AI_HARNESS", "")
	t.Setenv("AI_EDITOR", "")
	t.Setenv("WORK_CONTEXT", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeTOML(t, cfgPath, `
vault_path = "/tmp/main"
[[client]]
name = "Acme"
vault_path = "/vaults/acme"
  [[client.project]]
  project = "web"
  path = "projects/web"
  task_file = "tasks.md"
  notes_path = "notes"
  [[client.project]]
  project = "api"
  path = "projects/api"
  [[client.project]]
  project = "abs"
  path = "projects/abs"
  task_file = "/elsewhere/tasks.md"
  notes_path = "/elsewhere/notes"
`)
	cfg, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	// Relative task_file/notes_path resolve under vault + path.
	web, _ := cfg.ProjectByName("web")
	if web.File != "/vaults/acme/projects/web/tasks.md" {
		t.Errorf("web.File = %q", web.File)
	}
	if web.NotesPath != "/vaults/acme/projects/web/notes" {
		t.Errorf("web.NotesPath = %q", web.NotesPath)
	}

	// Defaults also resolve under path when task_file/notes_path are unset.
	api, _ := cfg.ProjectByName("api")
	if api.File != "/vaults/acme/projects/api/10-tasks/api.md" {
		t.Errorf("api.File = %q", api.File)
	}
	if api.NotesPath != "/vaults/acme/projects/api/00-inbox" {
		t.Errorf("api.NotesPath = %q", api.NotesPath)
	}

	// Absolute task_file/notes_path escape the path.
	abs, _ := cfg.ProjectByName("abs")
	if abs.File != "/elsewhere/tasks.md" {
		t.Errorf("abs.File = %q", abs.File)
	}
	if abs.NotesPath != "/elsewhere/notes" {
		t.Errorf("abs.NotesPath = %q", abs.NotesPath)
	}
}
