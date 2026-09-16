package config

// Launch-harness configuration: the harness config type, its TOML twin, the
// resolved target, and the four-tier resolution chain. Split out of the former
// config.go god-file (#60).

import (
	"errors"
	"fmt"
	"os"
)

// LaunchConfig describes the external AI harness/tool launched by
// `qi launch harness`. Resolved via Config.ResolveLaunchTarget: project override
// > client default > global [launch] > $AI_HARNESS/$AI_EDITOR.
type LaunchConfig struct {
	Harness string   `json:"harness"`        // executable resolved via PATH
	Args    []string `json:"args,omitempty"` // prepended before any pass-through args
	Detach  bool     `json:"detach"`         // true for GUI apps (spawn + return); false replaces qi (TUI)
}

type launchTOML struct {
	Harness string   `toml:"harness"`
	Args    []string `toml:"args"`
	Detach  bool     `toml:"detach"`
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
