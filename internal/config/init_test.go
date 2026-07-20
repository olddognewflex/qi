package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigTemplate_RoundTrips(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("QI_TASK_FILE_PATH", "")

	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tmpl := ConfigTemplate(vault)

	if !strings.Contains(tmpl, vault) {
		t.Fatalf("template missing vault path %q:\n%s", vault, tmpl)
	}
	// The vault_path line must be uncommented (it is the one required setting).
	if !strings.Contains(tmpl, "\nvault_path = ") {
		t.Fatalf("template has no active vault_path line:\n%s", tmpl)
	}

	// The generated template must parse back into a valid Config.
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(tmpl), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom(generated template): %v", err)
	}
	if cfg.VaultPath != vault {
		t.Errorf("VaultPath = %q, want %q", cfg.VaultPath, vault)
	}
	if want := filepath.Join(vault, "10-tasks", "inbox.md"); cfg.TaskFilePath != want {
		t.Errorf("TaskFilePath = %q, want %q", cfg.TaskFilePath, want)
	}
}

func TestWriteStarterConfig(t *testing.T) {
	t.Setenv("QI_VAULT_PATH", "")
	t.Setenv("QI_TASK_FILE_PATH", "")

	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)

	want := ConfigPath()
	if want != filepath.Join(cfgHome, "qi", "config.toml") {
		t.Fatalf("ConfigPath() = %q, unexpected under XDG override", want)
	}

	// First write succeeds and creates the parent dir.
	got, err := WriteStarterConfig("~/somewhere", false)
	if err != nil {
		t.Fatalf("WriteStarterConfig: %v", err)
	}
	if got != want {
		t.Errorf("wrote %q, want %q", got, want)
	}
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	// ~ must be expanded to an absolute path in the written file.
	if strings.Contains(string(data), `vault_path = "~/somewhere"`) {
		t.Errorf("vault_path was not expanded:\n%s", data)
	}
	if !strings.Contains(string(data), ExpandPath("~/somewhere")) {
		t.Errorf("expanded vault path missing from file:\n%s", data)
	}

	// Second write without force must refuse (no clobber).
	if _, err := WriteStarterConfig("~/elsewhere", false); err == nil {
		t.Fatal("WriteStarterConfig overwrote existing config without force")
	}

	// With force it overwrites.
	if _, err := WriteStarterConfig("~/elsewhere", true); err != nil {
		t.Fatalf("WriteStarterConfig(force): %v", err)
	}
	data, _ = os.ReadFile(want)
	if !strings.Contains(string(data), ExpandPath("~/elsewhere")) {
		t.Errorf("force did not rewrite vault path:\n%s", data)
	}
}

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	if got, want := ExpandPath("~/x"), filepath.Join(home, "x"); got != want {
		t.Errorf("ExpandPath(~/x) = %q, want %q", got, want)
	}
	if got := ExpandPath("rel"); !filepath.IsAbs(got) {
		t.Errorf("ExpandPath(rel) = %q, want absolute", got)
	}
}

func TestDefaultVaultPath_Absolute(t *testing.T) {
	if got := DefaultVaultPath(); !filepath.IsAbs(got) && got != "qi" {
		t.Errorf("DefaultVaultPath() = %q, want absolute", got)
	}
}
