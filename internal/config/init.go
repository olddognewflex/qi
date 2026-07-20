package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// ExpandPath resolves a leading ~ against the home directory and returns an
// absolute path. It is the exported form of the internal expandUserPath helper
// so callers (e.g. `qi init`) can normalise a vault path the same way the rest
// of the loader does.
func ExpandPath(path string) string {
	return expandUserPath(path)
}

// DefaultVaultPath is the vault location `qi init` proposes when the user does
// not supply one. It is an absolute path under the home directory.
func DefaultVaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "qi"
	}
	return filepath.Join(home, "qi")
}

// ConfigTemplate returns a commented starter config.toml with vaultPath filled
// into the (required) vault_path setting. Every other setting is shown commented
// with its default so a new user can discover the surface without reading docs.
func ConfigTemplate(vaultPath string) string {
	return fmt.Sprintf(`# qi configuration file.
#
# The only required setting is vault_path — the directory qi reads and writes
# markdown into. Everything below it is optional and shown with its default.
# Uncomment and edit the pieces you want; delete the rest.

# Path to your vault (required). An absolute path is safest; a leading ~ and
# relative paths are resolved against your home directory. Can be overridden
# with the QI_VAULT_PATH environment variable.
vault_path = %q

# Where `+"`qi task add`"+` writes tasks. Relative paths resolve inside the vault.
# Defaults to <vault>/10-tasks/inbox.md.
# task_file_path = "10-tasks/inbox.md"

# ── Daily notes ──────────────────────────────────────────────────────────────
# daily_dir_format  = "30-daily"     # subdirectory under the vault
# daily_file_format = "YYYY-MM-DD"   # daily-note filename pattern

# ── AI (opt-in) ──────────────────────────────────────────────────────────────
# qi never calls an LLM without you asking, and AI-proposed writes are always
# confirmation-gated. Configure a provider for `+"`qi ai run`"+`.
# [ai]
# provider = "anthropic"   # anthropic | ollama | openai | kimi | opencode | zai
# model    = "claude-sonnet-4-5"

# ── Calendars (all read-only) ────────────────────────────────────────────────
# [[ics_calendars]]
# name = "Work"
# url  = "https://example.com/calendar.ics"

# ── Semantic search (opt-in; embeds notes via a local Ollama model) ──────────
# [embeddings]
# enabled = true

# ── Background daemon: file watching + morning notifier ──────────────────────
# [sync]
# watch = true          # incrementally reconcile + reindex on vault changes
#
# [notify]
# due_today = true      # one macOS notification each morning with due tasks
# at        = "08:00"
`, vaultPath)
}

// WriteStarterConfig writes a commented starter config.toml (see ConfigTemplate)
// to the standard config location, creating the parent directory as needed. The
// supplied vaultPath is expanded to an absolute path before being embedded.
// It refuses to overwrite an existing config unless force is true, so callers
// cannot silently clobber a user's configuration. It returns the path written.
func WriteStarterConfig(vaultPath string, force bool) (string, error) {
	path := ConfigPath()
	if !force {
		if _, err := os.Stat(path); err == nil {
			return "", fmt.Errorf("config already exists at %s", path)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("creating config dir: %w", err)
	}
	content := ConfigTemplate(ExpandPath(vaultPath))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("writing config: %w", err)
	}
	return path, nil
}
