package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type MCPServer struct {
	ID      string
	Command string
	Args    []string
	Env     map[string]string
}

// RemoteQueueConfig configures the cloud queue that `qi remote-drain` pulls
// remote-captured tasks from. The cloud holds transient intent; the laptop is
// the only writer of the canonical vault. Token is the DRAIN token (pull/ack/
// deadletter scope).
type RemoteQueueConfig struct {
	Enabled bool
	URL     string
	Token   string
}

// SyncConfig configures qid's opt-in fsnotify-driven auto-reconcile. When Watch
// is true, qid watches the vault task dirs and runs the existing sync reconcile
// on change. DebounceMS coalesces bursts of writes; 0 lets the watcher apply its
// default (see internal/watcher.DefaultDebounce).
type SyncConfig struct {
	Watch      bool `json:"watch"`
	DebounceMS int  `json:"debounce_ms"`
}

// NotifyConfig configures qid's opt-in morning due-today notification. When
// DueToday is true, qid sends one macOS notification each morning at At
// (HH:MM, 24h) listing tasks due/scheduled for that day. An empty At lets the
// scheduler apply its default (see internal/notify.DefaultAt). Read-only.
type NotifyConfig struct {
	DueToday bool   `json:"due_today"`
	At       string `json:"at,omitempty"`
}

// AIConfig selects the LLM provider(s) used by `qi ai run`. Two shapes:
// the legacy single-provider keys (Provider/Model/...), and an ordered
// [[ai.providers]] failover chain (Providers) — first entry is primary,
// the rest are backups tried when a provider reports a usage limit or is
// unreachable. When Providers is non-empty it wins over the legacy keys;
// provider names are parsed by ai.ParseProvider.
type AIConfig struct {
	Provider    string             `json:"provider,omitempty"`
	Model       string             `json:"model,omitempty"`
	OllamaURL   string             `json:"ollama_url,omitempty"`
	OllamaModel string             `json:"ollama_model,omitempty"`
	Providers   []AIProviderConfig `json:"providers,omitempty"`
}

// AIProviderConfig is one [[ai.providers]] entry. Model is required for the
// OpenAI-compatible providers (openai/kimi/opencode/zai), which have no
// sensible cross-service default. URL and APIKeyEnv override the built-in
// endpoint and API-key env var (see ai.PresetFor); Ollama reads URL with
// the same meaning as the legacy ollama_url.
// APIKeyEnv is the NAME of an environment variable, not a secret, so it is safe
// to surface in `qi config show`.
type AIProviderConfig struct {
	Provider  string `json:"provider"`
	Model     string `json:"model,omitempty"`
	URL       string `json:"url,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
}

// EmbeddingsConfig configures opt-in local semantic search. When Enabled,
// `qi embed` builds per-note embeddings via Ollama (Model on OllamaURL) into the
// derived SQLite index, and `qi search --semantic` cosine-ranks against them.
// Empty Model/OllamaURL let the embed/search consumers apply their defaults
// (see internal/embed.DefaultModel / DefaultOllamaURL).
type EmbeddingsConfig struct {
	Enabled   bool   `json:"enabled"`
	Model     string `json:"model,omitempty"`
	OllamaURL string `json:"ollama_url,omitempty"`
}

type Config struct {
	VaultPath       string
	TaskFilePath    string
	InboxPath       string
	NotesPath       string
	DailyPath       string
	DailyDirFormat  string
	DailyFileFormat string
	ICSCalendars    []ICSCalendar
	CalDAVCalendars []CalDAVCalendar
	VdirCalendars   []VdirCalendar
	GoogleOAuth     GoogleOAuth
	GoogleCalendars []GoogleCalendar
	MCPServers      []MCPServer
	AI              AIConfig
	Clients         []ClientConfig
	Projects        []ProjectConfig
	Launch          LaunchConfig
	RemoteQueue     RemoteQueueConfig
	Sync            SyncConfig
	Notify          NotifyConfig
	Embeddings      EmbeddingsConfig
}

type mcpServerTOML struct {
	ID      string            `toml:"id"`
	Command string            `toml:"command"`
	Args    []string          `toml:"args"`
	Env     map[string]string `toml:"env"`
}

type aiTOML struct {
	Provider    string           `toml:"provider"`
	Model       string           `toml:"model"`
	OllamaURL   string           `toml:"ollama_url"`
	OllamaModel string           `toml:"ollama_model"`
	Providers   []aiProviderTOML `toml:"providers"`
}

type aiProviderTOML struct {
	Provider  string `toml:"provider"`
	Model     string `toml:"model"`
	URL       string `toml:"url"`
	APIKeyEnv string `toml:"api_key_env"`
}

// aiProviders copies [[ai.providers]] entries through, skipping ones with
// no provider name (matching the skip-don't-fail handling of incomplete
// calendar entries).
func aiProviders(raw []aiProviderTOML) []AIProviderConfig {
	out := make([]AIProviderConfig, 0, len(raw))
	for _, p := range raw {
		if strings.TrimSpace(p.Provider) == "" {
			continue
		}
		out = append(out, AIProviderConfig{
			Provider:  p.Provider,
			Model:     p.Model,
			URL:       p.URL,
			APIKeyEnv: p.APIKeyEnv,
		})
	}
	return out
}

type remoteQueueTOML struct {
	Enabled bool   `toml:"enabled"`
	URL     string `toml:"url"`
	Token   string `toml:"token"`
}

type syncTOML struct {
	Watch      bool `toml:"watch"`
	DebounceMS int  `toml:"debounce_ms"`
}

type notifyTOML struct {
	DueToday bool   `toml:"due_today"`
	At       string `toml:"at"`
}

type embeddingsTOML struct {
	Enabled   bool   `toml:"enabled"`
	Model     string `toml:"model"`
	OllamaURL string `toml:"ollama_url"`
}

type tomlFile struct {
	VaultPath       string          `toml:"vault_path"`
	TaskFilePath    string          `toml:"task_file_path"`
	DailyDirFormat  string          `toml:"daily_dir_format"`
	DailyFileFormat string          `toml:"daily_file_format"`
	ICSCalendars    []icsCalTOML    `toml:"ics_calendars"`
	CalDAVCalendars []caldavCalTOML `toml:"caldav_calendars"`
	VdirCalendars   []vdirCalTOML   `toml:"vdir_calendars"`
	GoogleOAuth     googleOAuthTOML `toml:"google_oauth"`
	GoogleCalendars []googleCalTOML `toml:"google_calendars"`
	MCPServers      []mcpServerTOML `toml:"mcp_servers"`
	AI              aiTOML          `toml:"ai"`
	Clients         []clientTOML    `toml:"client"`
	Launch          launchTOML      `toml:"launch"`
	RemoteQueue     remoteQueueTOML `toml:"remote_queue"`
	Sync            syncTOML        `toml:"sync"`
	Notify          notifyTOML      `toml:"notify"`
	Embeddings      embeddingsTOML  `toml:"embeddings"`
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

	// [remote_queue] env overrides, consistent with the QI_* pattern. The token
	// is preferably supplied via QI_QUEUE_TOKEN so it stays out of files synced
	// to the vault.
	if v := os.Getenv("QI_QUEUE_URL"); v != "" {
		raw.RemoteQueue.URL = v
	}
	if v := os.Getenv("QI_QUEUE_TOKEN"); v != "" {
		raw.RemoteQueue.Token = v
	}
	if v := os.Getenv("QI_QUEUE_ENABLED"); v == "1" || strings.EqualFold(v, "true") {
		raw.RemoteQueue.Enabled = true
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

	dailyDirFormat := raw.DailyDirFormat
	if dailyDirFormat == "" {
		dailyDirFormat = "30-daily"
	}
	dailyFileFormat := raw.DailyFileFormat
	if dailyFileFormat == "" {
		dailyFileFormat = "YYYY-MM-DD"
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

	vdirCalendars := make([]VdirCalendar, 0, len(raw.VdirCalendars))
	for _, c := range raw.VdirCalendars {
		if c.Name == "" || c.Path == "" {
			continue
		}
		vdirCalendars = append(vdirCalendars, VdirCalendar{
			Name:     c.Name,
			Path:     expandUserPath(c.Path),
			Discover: c.Discover,
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

	clients := make([]ClientConfig, 0, len(raw.Clients))
	var projects []ProjectConfig
	seenClients := make(map[string]struct{}, len(raw.Clients))
	seenProjects := make(map[string]struct{})
	seenFiles := make(map[string]struct{})
	for _, cl := range raw.Clients {
		if cl.Name == "" {
			return Config{}, fmt.Errorf("client: name is required")
		}
		if cl.VaultPath == "" {
			return Config{}, fmt.Errorf("client %q: vault_path is required", cl.Name)
		}
		if _, dup := seenClients[cl.Name]; dup {
			return Config{}, fmt.Errorf("client: duplicate name %q", cl.Name)
		}
		seenClients[cl.Name] = struct{}{}

		clientNotes := resolveNotesPath(cl.VaultPath, cl.NotesPath)

		// Optional client-level projection: a [[client]] with task_file becomes a
		// sync target tagged by the client name (a synthetic project), so
		// client-wide tasks (`qi task add --client`) route to it.
		var clientTaskFile string
		if cl.TaskFile != "" {
			clientTaskFile = cl.TaskFile
			if !filepath.IsAbs(clientTaskFile) {
				clientTaskFile = filepath.Join(cl.VaultPath, clientTaskFile)
			}
			if _, dup := seenProjects[cl.Name]; dup {
				return Config{}, fmt.Errorf("client %q: task_file project tag collides with a project named %q", cl.Name, cl.Name)
			}
			seenProjects[cl.Name] = struct{}{}
			if _, dup := seenFiles[clientTaskFile]; dup {
				return Config{}, fmt.Errorf("client %q: task_file resolves to duplicate path %q", cl.Name, clientTaskFile)
			}
			seenFiles[clientTaskFile] = struct{}{}

			projects = append(projects, ProjectConfig{
				Project:   cl.Name,
				Client:    cl.Name,
				VaultPath: cl.VaultPath,
				NotesPath: clientNotes,
				File:      clientTaskFile,
				Launch:    launchFromTOML(cl.Launch),
				synthetic: true,
			})
		}

		clients = append(clients, ClientConfig{
			Name:      cl.Name,
			VaultPath: cl.VaultPath,
			NotesPath: clientNotes,
			DevRoot:   cl.DevRoot,
			TaskFile:  clientTaskFile,
			Launch:    launchFromTOML(cl.Launch),
		})

		for _, p := range cl.Projects {
			if p.Project == "" {
				return Config{}, fmt.Errorf("client %q: project is required", cl.Name)
			}
			if _, dup := seenProjects[p.Project]; dup {
				return Config{}, fmt.Errorf("project: duplicate project %q", p.Project)
			}
			seenProjects[p.Project] = struct{}{}

			vaultPath := p.VaultPath
			if vaultPath == "" {
				vaultPath = cl.VaultPath
			}

			// base is the project's root for relative task_file/notes_path (and
			// their defaults): vault + path. Empty path leaves base at the vault
			// root. Absolute task_file/notes_path escape it.
			base := vaultPath
			if p.Path != "" {
				if filepath.IsAbs(p.Path) {
					base = p.Path
				} else {
					base = filepath.Join(vaultPath, p.Path)
				}
			}

			devPath := p.DevPath
			if devPath != "" && !filepath.IsAbs(devPath) {
				if cl.DevRoot == "" {
					return Config{}, fmt.Errorf("project %q: relative dev_path %q needs dev_root on client %q", p.Project, devPath, cl.Name)
				}
				devPath = filepath.Join(cl.DevRoot, devPath)
			}

			file := p.File
			if file == "" {
				flatName := strings.ReplaceAll(p.Project, "/", "-")
				file = filepath.Join("10-tasks", flatName+".md")
			}
			if !filepath.IsAbs(file) {
				file = filepath.Join(base, file)
			}
			if _, dup := seenFiles[file]; dup {
				return Config{}, fmt.Errorf("project: duplicate resolved file path %q", file)
			}
			seenFiles[file] = struct{}{}

			projects = append(projects, ProjectConfig{
				Project:   p.Project,
				Client:    cl.Name,
				VaultPath: vaultPath,
				NotesPath: resolveNotesPath(base, p.NotesPath, cl.NotesPath),
				DevPath:   devPath,
				File:      file,
				Launch:    launchFromTOML(p.Launch),
			})
		}
	}

	return Config{
		VaultPath:       raw.VaultPath,
		TaskFilePath:    taskFilePath,
		InboxPath:       filepath.Join(raw.VaultPath, "00-inbox"),
		NotesPath:       filepath.Join(raw.VaultPath, "20-notes"),
		DailyPath:       filepath.Join(raw.VaultPath, "30-daily"),
		DailyDirFormat:  dailyDirFormat,
		DailyFileFormat: dailyFileFormat,
		ICSCalendars:    icsCalendars,
		CalDAVCalendars: caldavCalendars,
		VdirCalendars:   vdirCalendars,
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
			Providers:   aiProviders(raw.AI.Providers),
		},
		Clients:  clients,
		Projects: projects,
		Launch: LaunchConfig{
			Harness: raw.Launch.Harness,
			Args:    raw.Launch.Args,
			Detach:  raw.Launch.Detach,
		},
		RemoteQueue: RemoteQueueConfig{
			Enabled: raw.RemoteQueue.Enabled,
			URL:     raw.RemoteQueue.URL,
			Token:   raw.RemoteQueue.Token,
		},
		// Keep the raw debounce; the default is applied by the watcher, not here.
		Sync: SyncConfig{Watch: raw.Sync.Watch, DebounceMS: raw.Sync.DebounceMS},
		// Keep the raw At; the default is applied by the scheduler, not here.
		Notify: NotifyConfig{DueToday: raw.Notify.DueToday, At: raw.Notify.At},
		// Keep the raw Model/OllamaURL; defaults are applied by the embed/search
		// consumers (internal/embed), not here.
		Embeddings: EmbeddingsConfig{
			Enabled:   raw.Embeddings.Enabled,
			Model:     raw.Embeddings.Model,
			OllamaURL: raw.Embeddings.OllamaURL,
		},
	}, nil
}

var tokenRe = regexp.MustCompile(`YYYY|MMMM|MMM|MM|DD`)

// resolveDateFormat substitutes Obsidian/moment date tokens with their values
// for day, leaving literal text untouched. Only matched tokens are replaced —
// it does NOT pass the format through time.Format, which would misinterpret
// literal path text (e.g. the "3" in "30-daily") as Go layout components. The
// regexp alternation is ordered longest-first so MMMM/MMM resolve before MM.
func resolveDateFormat(format string, day time.Time) string {
	return tokenRe.ReplaceAllStringFunc(format, func(tok string) string {
		switch tok {
		case "YYYY":
			return day.Format("2006")
		case "MMMM":
			return day.Format("January")
		case "MMM":
			return day.Format("Jan")
		case "MM":
			return day.Format("01")
		case "DD":
			return day.Format("02")
		}
		return tok
	})
}

// DailyNotePath resolves the absolute path to the daily note for day, using the
// configured folder and filename formats (Obsidian date tokens).
func (c Config) DailyNotePath(day time.Time) string {
	return filepath.Join(
		c.VaultPath,
		resolveDateFormat(c.DailyDirFormat, day),
		resolveDateFormat(c.DailyFileFormat, day)+".md",
	)
}

// expandUserPath resolves a leading ~ against the home dir and makes the result
// absolute, so a config path is usable regardless of the process working dir.
func expandUserPath(path string) string {
	if path == "" {
		return path
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

func DataDir() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "qi")
}
