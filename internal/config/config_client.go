package config

// Client/project configuration: the client grouping and its flattened projects,
// their TOML wire twins, and the name-resolution helpers. Split out of the
// former config.go god-file (#60). Flattening itself stays inline in Load.

import (
	"errors"
	"fmt"
	"path/filepath"
)

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
