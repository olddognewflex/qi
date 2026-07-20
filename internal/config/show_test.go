package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactViewRedactsSecrets(t *testing.T) {
	c := Config{
		VaultPath: "/vault",
		CalDAVCalendars: []CalDAVCalendar{
			{Name: "work", Endpoint: "https://dav", Username: "me", Password: "hunter2", Path: "/cal"},
			{Name: "empty", Endpoint: "https://dav2", Username: "you"}, // no password set
		},
		GoogleOAuth: GoogleOAuth{ClientID: "cid.apps", ClientSecret: "gsecret"},
		MCPServers: []MCPServer{
			{ID: "s1", Command: "srv", Args: []string{"--x"}, Env: map[string]string{"API_KEY": "sk-123", "DEBUG": "1"}},
		},
		RemoteQueue: RemoteQueueConfig{Enabled: true, URL: "https://q", Token: "drain-tok"},
	}

	v := RedactView(c, "/etc/qi/config.toml")

	if v.ConfigPath != "/etc/qi/config.toml" {
		t.Errorf("config path = %q", v.ConfigPath)
	}
	// Secrets that are set → marker; the surrounding non-secret fields intact.
	if v.CalDAVCalendars[0].Password != RedactedMarker {
		t.Errorf("caldav password not redacted: %q", v.CalDAVCalendars[0].Password)
	}
	if v.CalDAVCalendars[0].Username != "me" || v.CalDAVCalendars[0].Endpoint != "https://dav" {
		t.Errorf("caldav non-secret fields altered: %+v", v.CalDAVCalendars[0])
	}
	// Unset secret stays empty so presence is visible (not the marker).
	if v.CalDAVCalendars[1].Password != "" {
		t.Errorf("unset caldav password should stay empty, got %q", v.CalDAVCalendars[1].Password)
	}
	if v.GoogleOAuth.ClientSecret != RedactedMarker || v.GoogleOAuth.ClientID != "cid.apps" {
		t.Errorf("google oauth redaction wrong: %+v", v.GoogleOAuth)
	}
	if v.RemoteQueue.Token != RedactedMarker || v.RemoteQueue.URL != "https://q" || !v.RemoteQueue.Enabled {
		t.Errorf("remote queue redaction wrong: %+v", v.RemoteQueue)
	}
	// MCP env values redacted, keys and command preserved.
	env := v.MCPServers[0].Env
	if env["API_KEY"] != RedactedMarker || env["DEBUG"] != RedactedMarker {
		t.Errorf("mcp env values not redacted: %+v", env)
	}
	if v.MCPServers[0].Command != "srv" {
		t.Errorf("mcp command altered: %q", v.MCPServers[0].Command)
	}

	// No original secret value survives anywhere in the serialized output.
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, secret := range []string{"hunter2", "gsecret", "sk-123", "drain-tok"} {
		if strings.Contains(string(b), secret) {
			t.Errorf("secret %q leaked into config show output: %s", secret, b)
		}
	}
}

func TestRedactViewSurfacesSyntheticProjects(t *testing.T) {
	c := Config{
		VaultPath: "/vault",
		Projects: []ProjectConfig{
			{Project: "real", Client: "acme", VaultPath: "/vault", File: "/vault/10-tasks/real.md"},
			{Project: "acme", Client: "acme", VaultPath: "/vault", File: "/vault/10-tasks/acme.md", synthetic: true},
		},
	}
	v := RedactView(c, "")
	if len(v.Projects) != 2 {
		t.Fatalf("want 2 projects, got %d", len(v.Projects))
	}
	if v.Projects[0].Synthetic {
		t.Errorf("real project should not be synthetic")
	}
	if !v.Projects[1].Synthetic {
		t.Errorf("client-projection project should be marked synthetic")
	}
}
