package config

// This file builds the redacted, JSON-serializable view of a resolved Config
// for `qi config show`. It lives in the config package (not commands) because
// it needs to read the unexported `synthetic` project flag and it owns the
// knowledge of which fields are secret. The wire shape (ShowConfig and its
// show* sub-DTOs) is a stable introspection surface, deliberately separate from
// the domain structs so secrets never leak and internal fields never surface.

// RedactedMarker replaces any set secret value in `qi config show` output. An
// unset secret is left empty so the output still reveals whether the secret is
// configured, without disclosing its value.
const RedactedMarker = "<redacted>"

// redact masks a secret: a set value becomes the marker, an unset one stays
// empty.
func redact(s string) string {
	if s == "" {
		return ""
	}
	return RedactedMarker
}

// ShowConfig is the fully-resolved, secrets-redacted view of a Config.
type ShowConfig struct {
	ConfigPath      string           `json:"config_path"`
	VaultPath       string           `json:"vault_path"`
	TaskFilePath    string           `json:"task_file_path"`
	InboxPath       string           `json:"inbox_path"`
	NotesPath       string           `json:"notes_path"`
	DailyPath       string           `json:"daily_path"`
	DailyDirFormat  string           `json:"daily_dir_format"`
	DailyFileFormat string           `json:"daily_file_format"`
	ICSCalendars    []ICSCalendar    `json:"ics_calendars,omitempty"`
	CalDAVCalendars []showCalDAV     `json:"caldav_calendars,omitempty"`
	VdirCalendars   []VdirCalendar   `json:"vdir_calendars,omitempty"`
	GoogleOAuth     showGoogleOAuth  `json:"google_oauth"`
	GoogleCalendars []GoogleCalendar `json:"google_calendars,omitempty"`
	MCPServers      []showMCPServer  `json:"mcp_servers,omitempty"`
	AI              AIConfig         `json:"ai"`
	Launch          LaunchConfig     `json:"launch"`
	Clients         []showClient     `json:"clients,omitempty"`
	Projects        []showProject    `json:"projects,omitempty"`
	RemoteQueue     showRemoteQueue  `json:"remote_queue"`
	Sync            SyncConfig       `json:"sync"`
	Notify          NotifyConfig     `json:"notify"`
	Embeddings      EmbeddingsConfig `json:"embeddings"`
}

// showCalDAV redacts the CalDAV password (username/endpoint/path are not secret).
type showCalDAV struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	Username string `json:"username"`
	Password string `json:"password,omitempty"` // redacted
	Path     string `json:"path,omitempty"`
}

// showGoogleOAuth surfaces the client id (not secret) and redacts the secret.
type showGoogleOAuth struct {
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"` // redacted
}

// showMCPServer surfaces the command/args and env var names, redacting env
// values (which commonly carry API keys).
type showMCPServer struct {
	ID      string            `json:"id"`
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"` // values redacted
}

// showRemoteQueue redacts the drain token.
type showRemoteQueue struct {
	Enabled bool   `json:"enabled"`
	URL     string `json:"url,omitempty"`
	Token   string `json:"token,omitempty"` // redacted
}

// showClient mirrors ClientConfig; it holds no secrets but resolves the
// optional launch override to a plain (possibly null) value.
type showClient struct {
	Name      string        `json:"name"`
	VaultPath string        `json:"vault_path"`
	NotesPath string        `json:"notes_path"`
	DevRoot   string        `json:"dev_root,omitempty"`
	TaskFile  string        `json:"task_file,omitempty"`
	Launch    *LaunchConfig `json:"launch,omitempty"`
}

// showProject mirrors ProjectConfig and additionally surfaces the otherwise
// unexported `synthetic` flag — the client-projection projects excluded from
// name resolution, whose invisibility is a documented source of confusion.
type showProject struct {
	Project   string        `json:"project"`
	Client    string        `json:"client,omitempty"`
	VaultPath string        `json:"vault_path"`
	NotesPath string        `json:"notes_path"`
	DevPath   string        `json:"dev_path,omitempty"`
	File      string        `json:"task_file"`
	Launch    *LaunchConfig `json:"launch,omitempty"`
	Synthetic bool          `json:"synthetic"`
}

// RedactView builds the secrets-redacted introspection view of c. configPath is
// the file c was loaded from (informational; may not exist).
func RedactView(c Config, configPath string) ShowConfig {
	caldav := make([]showCalDAV, 0, len(c.CalDAVCalendars))
	for _, cal := range c.CalDAVCalendars {
		caldav = append(caldav, showCalDAV{
			Name:     cal.Name,
			Endpoint: cal.Endpoint,
			Username: cal.Username,
			Password: redact(cal.Password),
			Path:     cal.Path,
		})
	}

	mcp := make([]showMCPServer, 0, len(c.MCPServers))
	for _, s := range c.MCPServers {
		var env map[string]string
		if len(s.Env) > 0 {
			env = make(map[string]string, len(s.Env))
			for k, v := range s.Env {
				env[k] = redact(v)
			}
		}
		mcp = append(mcp, showMCPServer{ID: s.ID, Command: s.Command, Args: s.Args, Env: env})
	}

	clients := make([]showClient, 0, len(c.Clients))
	for _, cl := range c.Clients {
		clients = append(clients, showClient{
			Name:      cl.Name,
			VaultPath: cl.VaultPath,
			NotesPath: cl.NotesPath,
			DevRoot:   cl.DevRoot,
			TaskFile:  cl.TaskFile,
			Launch:    cl.Launch,
		})
	}

	projects := make([]showProject, 0, len(c.Projects))
	for _, p := range c.Projects {
		projects = append(projects, showProject{
			Project:   p.Project,
			Client:    p.Client,
			VaultPath: p.VaultPath,
			NotesPath: p.NotesPath,
			DevPath:   p.DevPath,
			File:      p.File,
			Launch:    p.Launch,
			Synthetic: p.synthetic,
		})
	}

	return ShowConfig{
		ConfigPath:      configPath,
		VaultPath:       c.VaultPath,
		TaskFilePath:    c.TaskFilePath,
		InboxPath:       c.InboxPath,
		NotesPath:       c.NotesPath,
		DailyPath:       c.DailyPath,
		DailyDirFormat:  c.DailyDirFormat,
		DailyFileFormat: c.DailyFileFormat,
		ICSCalendars:    c.ICSCalendars,
		CalDAVCalendars: caldav,
		VdirCalendars:   c.VdirCalendars,
		GoogleOAuth: showGoogleOAuth{
			ClientID:     c.GoogleOAuth.ClientID,
			ClientSecret: redact(c.GoogleOAuth.ClientSecret),
		},
		GoogleCalendars: c.GoogleCalendars,
		MCPServers:      mcp,
		AI:              c.AI,
		Launch:          c.Launch,
		Clients:         clients,
		Projects:        projects,
		RemoteQueue: showRemoteQueue{
			Enabled: c.RemoteQueue.Enabled,
			URL:     c.RemoteQueue.URL,
			Token:   redact(c.RemoteQueue.Token),
		},
		Sync:       c.Sync,
		Notify:     c.Notify,
		Embeddings: c.Embeddings,
	}
}
