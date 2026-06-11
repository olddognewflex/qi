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

// ClientConfig is a client grouping: one Obsidian vault and one dev root shared
// across the client's projects. Projects are flattened into Config.Projects at
// load; ClientConfig is retained so `qi launch harness` can resolve a launch
// from a client name alone (cwd = DevRoot).
type ClientConfig struct {
	Name      string
	VaultPath string        // Obsidian notes vault for all the client's projects
	NotesPath string        // resolved dir for `qi note new --client`; default <vault>/00-inbox
	DevRoot   string        // root dev folder; relative project dev_path resolves under it
	TaskFile  string        // optional client-level projection task file (absolute, resolved)
	Launch    *LaunchConfig // client default harness; nil = inherit global [launch]
}

// ProjectConfig is a single project, flattened from its [[client]] parent with
// VaultPath/DevPath resolved to absolute paths and Launch/Client inherited.
type ProjectConfig struct {
	Project   string
	Client    string // owning client name (for harness inheritance)
	VaultPath string
	NotesPath string // resolved dir for `qi note new --project`; default <vault>/00-inbox
	DevPath   string // optional working dir for `qi launch harness`; empty = don't chdir
	File      string
	Launch    *LaunchConfig // project-level override; nil = inherit client/global

	// synthetic marks the client-level projection (from [[client]] task_file)
	// that is flattened into Projects for sync routing. It is excluded from
	// ProjectByName so it never shadows client-name launch resolution.
	synthetic bool
}

// LaunchConfig describes the external AI harness/tool launched by
// `qi launch harness`. Resolved via Config.ResolveLaunchTarget: project override
// > client default > global [launch] > $AI_HARNESS/$AI_EDITOR.
type LaunchConfig struct {
	Harness string   // executable resolved via PATH
	Args    []string // prepended before any pass-through args
	Detach  bool     // true for GUI apps (spawn + return); false replaces qi (TUI)
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
	Watch      bool
	DebounceMS int
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
	DailyDirFormat  string
	DailyFileFormat string
	ICSCalendars    []ICSCalendar
	CalDAVCalendars []CalDAVCalendar
	GoogleOAuth     GoogleOAuth
	GoogleCalendars []GoogleCalendar
	MCPServers      []MCPServer
	AI              AIConfig
	Clients         []ClientConfig
	Projects        []ProjectConfig
	Launch          LaunchConfig
	RemoteQueue     RemoteQueueConfig
	Sync            SyncConfig
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

type launchTOML struct {
	Harness string   `toml:"harness"`
	Args    []string `toml:"args"`
	Detach  bool     `toml:"detach"`
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

type projectTOML struct {
	Project   string      `toml:"project"`
	VaultPath string      `toml:"vault_path"` // optional; overrides the client vault
	Path      string      `toml:"path"`       // optional; project base subdir within the vault for relative task_file/notes_path
	NotesPath string      `toml:"notes_path"` // optional; note dir, default 00-inbox (inherits client)
	DevPath   string      `toml:"dev_path"`   // absolute, or relative to client dev_root
	File      string      `toml:"task_file"`  // projection task file; default 10-tasks/<project>.md
	Launch    *launchTOML `toml:"launch"`
}

type clientTOML struct {
	Name      string        `toml:"name"`
	VaultPath string        `toml:"vault_path"`
	NotesPath string        `toml:"notes_path"` // optional; note dir, default 00-inbox
	DevRoot   string        `toml:"dev_root"`
	TaskFile  string        `toml:"task_file"` // optional client-level projection file
	Launch    *launchTOML   `toml:"launch"`
	Projects  []projectTOML `toml:"project"`
}

type tomlFile struct {
	VaultPath       string          `toml:"vault_path"`
	TaskFilePath    string          `toml:"task_file_path"`
	DailyDirFormat  string          `toml:"daily_dir_format"`
	DailyFileFormat string          `toml:"daily_file_format"`
	ICSCalendars    []icsCalTOML    `toml:"ics_calendars"`
	CalDAVCalendars []caldavCalTOML `toml:"caldav_calendars"`
	GoogleOAuth     googleOAuthTOML `toml:"google_oauth"`
	GoogleCalendars []googleCalTOML `toml:"google_calendars"`
	MCPServers      []mcpServerTOML `toml:"mcp_servers"`
	AI              aiTOML          `toml:"ai"`
	Clients         []clientTOML    `toml:"client"`
	Launch          launchTOML      `toml:"launch"`
	RemoteQueue     remoteQueueTOML `toml:"remote_queue"`
	Sync            syncTOML        `toml:"sync"`
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
	}, nil
}

// resolveNotesPath picks the first non-empty candidate (e.g. project then client
// notes_path), defaulting to "00-inbox", and resolves it absolute against
// vaultPath when relative.
func resolveNotesPath(vaultPath string, candidates ...string) string {
	dir := "00-inbox"
	for _, c := range candidates {
		if c != "" {
			dir = c
			break
		}
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(vaultPath, dir)
	}
	return dir
}

// launchFromTOML converts an optional [launch] table into a *LaunchConfig,
// returning nil when absent or harness-less so callers fall through to the next
// resolution tier.
func launchFromTOML(l *launchTOML) *LaunchConfig {
	if l == nil || l.Harness == "" {
		return nil
	}
	return &LaunchConfig{Harness: l.Harness, Args: l.Args, Detach: l.Detach}
}

// ProjectByName returns the configured project with the given name. Synthetic
// client-level projections are skipped so they never shadow client-name launch
// resolution. The second return is false when no project matches (including when
// name is "").
func (c Config) ProjectByName(name string) (ProjectConfig, bool) {
	if name == "" {
		return ProjectConfig{}, false
	}
	for _, p := range c.Projects {
		if p.synthetic {
			continue
		}
		if p.Project == name {
			return p, true
		}
	}
	return ProjectConfig{}, false
}

// NoteVaultFor resolves where a new note belongs: the vault root (for display)
// and the notes directory to write into (the vault's configured notes_path).
// client and project are mutually exclusive flag values; an empty pair resolves
// to the main vault. Unknown names error.
func (c Config) NoteVaultFor(client, project string) (vaultPath, notesDir string, err error) {
	switch {
	case client != "" && project != "":
		return "", "", errors.New("note: --client and --project are mutually exclusive")
	case client != "":
		cl, ok := c.ClientByName(client)
		if !ok {
			return "", "", fmt.Errorf("note: unknown client %q", client)
		}
		return cl.VaultPath, cl.NotesPath, nil
	case project != "":
		p, ok := c.ProjectByName(project)
		if !ok {
			return "", "", fmt.Errorf("note: unknown project %q", project)
		}
		return p.VaultPath, p.NotesPath, nil
	default:
		return c.VaultPath, c.NotesPath, nil
	}
}

// ClientByName returns the configured client with the given name. The second
// return is false when no client matches (including when name is "").
func (c Config) ClientByName(name string) (ClientConfig, bool) {
	if name == "" {
		return ClientConfig{}, false
	}
	for _, cl := range c.Clients {
		if cl.Name == name {
			return cl, true
		}
	}
	return ClientConfig{}, false
}

// LaunchTarget is the resolved launch context: which harness to run, the vault
// to export as QI_VAULT_PATH, and the working directory (empty = current dir).
type LaunchTarget struct {
	Harness   LaunchConfig
	VaultPath string
	WorkDir   string
	Label     string // human label of what matched, e.g. `project "BHQ"`; empty for the global default
	FromEnv   bool   // the matched name came from $WORK_CONTEXT, not an explicit flag
}

// ResolveLaunchTarget resolves the launch context for `qi launch harness`. The
// name (an explicit --project flag, else $WORK_CONTEXT) is matched project-first,
// then client:
//
//   - project match → harness [project.launch] > [client.launch] > [launch] > env;
//     vault = project vault; cwd = project dev_path (may be empty)
//   - client match  → harness [client.launch] > [launch] > env;
//     vault = client vault; cwd = client dev_root
//   - no name       → global [launch] > env; vault = global vault; cwd = current dir
//
// An explicit flag that matches neither is an error; an unmatched $WORK_CONTEXT
// is lenient and falls through to the global default.
func (c Config) ResolveLaunchTarget(flag string) (LaunchTarget, error) {
	name := flag
	fromEnv := false
	if name == "" {
		name = os.Getenv("WORK_CONTEXT")
		fromEnv = name != ""
	}

	if name != "" {
		if p, ok := c.ProjectByName(name); ok {
			var clientLaunch *LaunchConfig
			if cl, ok := c.ClientByName(p.Client); ok {
				clientLaunch = cl.Launch
			}
			h, err := c.harnessFrom(p.Launch, clientLaunch)
			if err != nil {
				return LaunchTarget{}, err
			}
			return LaunchTarget{Harness: h, VaultPath: p.VaultPath, WorkDir: p.DevPath, Label: fmt.Sprintf("project %q", p.Project), FromEnv: fromEnv}, nil
		}
		if cl, ok := c.ClientByName(name); ok {
			h, err := c.harnessFrom(cl.Launch)
			if err != nil {
				return LaunchTarget{}, err
			}
			return LaunchTarget{Harness: h, VaultPath: cl.VaultPath, WorkDir: cl.DevRoot, Label: fmt.Sprintf("client %q", cl.Name), FromEnv: fromEnv}, nil
		}
		if !fromEnv {
			return LaunchTarget{}, fmt.Errorf("launch: unknown project or client %q", name)
		}
		// Unmatched $WORK_CONTEXT falls through to the global default.
	}

	h, err := c.harnessFrom()
	if err != nil {
		return LaunchTarget{}, err
	}
	return LaunchTarget{Harness: h, VaultPath: c.VaultPath}, nil
}

// harnessFrom returns the first override with a non-empty harness, else the
// global [launch] block, else $AI_HARNESS, else $AI_EDITOR. Errors when none is
// configured at any tier.
func (c Config) harnessFrom(overrides ...*LaunchConfig) (LaunchConfig, error) {
	for _, o := range overrides {
		if o != nil && o.Harness != "" {
			return *o, nil
		}
	}
	if c.Launch.Harness != "" {
		return c.Launch, nil
	}
	if h := os.Getenv("AI_HARNESS"); h != "" {
		return LaunchConfig{Harness: h}, nil
	}
	if h := os.Getenv("AI_EDITOR"); h != "" {
		return LaunchConfig{Harness: h}, nil
	}
	return LaunchConfig{}, errors.New("launch: no harness configured (set [launch] harness in config.toml or $AI_HARNESS)")
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

func DataDir() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "qi")
}
