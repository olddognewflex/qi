package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	"qi/internal/config"
)

func newLaunchCommand(cfg config.Config) *cobra.Command {
	launchCmd := &cobra.Command{
		Use:   "launch",
		Short: "Launch external tools in vault/project context",
	}
	launchCmd.AddCommand(newLaunchHarnessCommand(cfg))
	return launchCmd
}

func newLaunchHarnessCommand(cfg config.Config) *cobra.Command {
	var project string
	var client string
	var printOnly bool
	var asJSON bool

	cmd := &cobra.Command{
		Use:     "harness [-- harness-args...]",
		Aliases: []string{"ai"},
		Short:   "Launch the configured AI harness for this project",
		Long: "Resolve and launch the AI harness. The target (--project, else\n" +
			"$WORK_CONTEXT) is matched project-first, then client:\n" +
			"  project → harness [project.launch] > [client.launch] > [launch] > env;\n" +
			"            cwd = project dev_path\n" +
			"  client  → harness [client.launch] > [launch] > env; cwd = client dev_root\n" +
			"  neither → global [launch] > env; cwd = current dir\n" +
			"QI_VAULT_PATH is exported (the matched vault, else the global vault).\n" +
			"Extra args after -- are passed through to the harness.\n" +
			"With --print, resolve and show the harness/vault/cwd without executing.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// --client is sugar for resolving a client by name; it is validated
			// as a client and is mutually exclusive with --project.
			target := project
			if client != "" {
				if project != "" {
					return fmt.Errorf("--client and --project are mutually exclusive")
				}
				if _, ok := cfg.ClientByName(client); !ok {
					return fmt.Errorf("unknown client %q", client)
				}
				target = client
			}
			tgt, err := cfg.ResolveLaunchTarget(target)
			if err != nil {
				return err
			}
			if printOnly {
				return printLaunchTarget(cmd, tgt, args, asJSON)
			}
			if tgt.FromEnv && tgt.Label != "" {
				// Matched from $WORK_CONTEXT — note it so the resolved harness
				// isn't a surprise (especially before an exec handoff).
				fmt.Fprintf(cmd.ErrOrStderr(), "qi: using %s (from $WORK_CONTEXT)\n", tgt.Label)
			}
			return runHarness(cmd, tgt.Harness, tgt.VaultPath, tgt.WorkDir, args)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "project or client to resolve the harness for (defaults to $WORK_CONTEXT)")
	cmd.Flags().StringVar(&client, "client", "", "client to resolve the harness for (validated; cwd = client dev_root)")
	cmd.Flags().BoolVar(&printOnly, "print", false, "print the resolved harness/vault/cwd without executing")
	cmd.Flags().BoolVar(&asJSON, "json", false, "with --print, emit the resolution as JSON")
	return cmd
}

// launchResolutionJSON is the stable JSON shape of a resolved launch target for
// `qi launch harness --print --json`.
type launchResolutionJSON struct {
	Matched  string   `json:"matched"`               // human label, e.g. `project "BHQ"`; empty for the global default
	FromEnv  bool     `json:"from_env"`              // the target name came from $WORK_CONTEXT
	Harness  string   `json:"harness"`               // executable that would be launched
	Args     []string `json:"args,omitempty"`        // configured args prepended before passthrough
	Passthru []string `json:"passthrough,omitempty"` // args after --
	Detach   bool     `json:"detach"`                // GUI spawn (true) vs exec-replace (false)
	Vault    string   `json:"vault"`                 // QI_VAULT_PATH that would be exported
	Cwd      string   `json:"cwd"`                   // working dir; "" means the current dir is inherited
}

// printLaunchTarget renders a resolved launch target without executing it, as a
// human summary or (with asJSON) a stable JSON object. It does NOT resolve the
// harness against PATH — that is runHarness's job at exec time — so --print
// reports the configured intent even when the binary is missing.
func printLaunchTarget(cmd *cobra.Command, tgt config.LaunchTarget, passthrough []string, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(launchResolutionJSON{
			Matched:  tgt.Label,
			FromEnv:  tgt.FromEnv,
			Harness:  tgt.Harness.Harness,
			Args:     tgt.Harness.Args,
			Passthru: passthrough,
			Detach:   tgt.Harness.Detach,
			Vault:    tgt.VaultPath,
			Cwd:      tgt.WorkDir,
		})
	}

	out := cmd.OutOrStdout()
	matched := tgt.Label
	if matched == "" {
		matched = "global default"
	}
	if tgt.FromEnv {
		matched += " (from $WORK_CONTEXT)"
	}
	fmt.Fprintf(out, "matched:  %s\n", matched)
	harness := tgt.Harness.Harness
	if len(tgt.Harness.Args) > 0 {
		harness += " " + strings.Join(tgt.Harness.Args, " ")
	}
	if len(passthrough) > 0 {
		harness += " " + strings.Join(passthrough, " ")
	}
	fmt.Fprintf(out, "harness:  %s\n", harness)
	fmt.Fprintf(out, "detach:   %t\n", tgt.Harness.Detach)
	fmt.Fprintf(out, "vault:    %s\n", tgt.VaultPath)
	cwd := tgt.WorkDir
	if cwd == "" {
		cwd = "(current dir)"
	}
	fmt.Fprintf(out, "cwd:      %s\n", cwd)
	return nil
}

// runHarness launches lc.Harness with QI_VAULT_PATH=vaultPath exported. workDir
// is the harness's working directory; when empty the current dir is inherited
// (no chdir). passthrough args follow lc.Args. Detached harnesses (GUI apps) are
// spawned and control returns to the caller; non-detached harnesses (TUI apps)
// replace the qi process entirely for a clean handoff.
func runHarness(cmd *cobra.Command, lc config.LaunchConfig, vaultPath, workDir string, passthrough []string) error {
	bin, err := exec.LookPath(lc.Harness)
	if err != nil {
		return fmt.Errorf("launch: harness %q not found in PATH: %w", lc.Harness, err)
	}

	argv := make([]string, 0, len(lc.Args)+len(passthrough))
	argv = append(argv, lc.Args...)
	argv = append(argv, passthrough...)

	env := append(os.Environ(), "QI_VAULT_PATH="+vaultPath)

	if lc.Detach {
		c := exec.Command(bin, argv...)
		c.Dir = workDir // empty = inherit current dir
		c.Env = env
		if err := c.Start(); err != nil {
			return fmt.Errorf("launch: starting %q: %w", lc.Harness, err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Launched %s (pid %d).\n", lc.Harness, c.Process.Pid)
		return c.Process.Release()
	}

	return execReplace(bin, argv, env, workDir)
}
