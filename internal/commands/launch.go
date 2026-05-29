package commands

import (
	"fmt"
	"os"
	"os/exec"

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
			"Extra args after -- are passed through to the harness.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			tgt, err := cfg.ResolveLaunchTarget(project)
			if err != nil {
				return err
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
	return cmd
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
