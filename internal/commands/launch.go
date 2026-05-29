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
		Long: "Resolve and launch the AI harness. Resolution order:\n" +
			"  --project <name> [project_vault.launch] > [launch] > $AI_HARNESS > $AI_EDITOR\n" +
			"The harness runs with cwd set to the vault and QI_VAULT_PATH exported.\n" +
			"Extra args after -- are passed through to the harness.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			effective := cfg.EffectiveProject(project)
			lc, err := cfg.ResolveLaunch(effective)
			if err != nil {
				return err
			}
			if project == "" && effective != "" {
				// effective came from $WORK_CONTEXT — note it so the resolved
				// harness isn't a surprise (especially before an exec handoff).
				fmt.Fprintf(cmd.ErrOrStderr(), "qi: using project %q (from $WORK_CONTEXT)\n", effective)
			}
			return runHarness(cmd, cfg, lc, args)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "project_vault to resolve the harness for (defaults to $WORK_CONTEXT)")
	return cmd
}

// runHarness launches lc.Harness in the vault directory with QI_VAULT_PATH set.
// passthrough args follow lc.Args. Detached harnesses (GUI apps) are spawned and
// control returns to the caller; non-detached harnesses (TUI apps) replace the
// qi process entirely for a clean handoff.
func runHarness(cmd *cobra.Command, cfg config.Config, lc config.LaunchConfig, passthrough []string) error {
	bin, err := exec.LookPath(lc.Harness)
	if err != nil {
		return fmt.Errorf("launch: harness %q not found in PATH: %w", lc.Harness, err)
	}

	argv := make([]string, 0, len(lc.Args)+len(passthrough))
	argv = append(argv, lc.Args...)
	argv = append(argv, passthrough...)

	env := append(os.Environ(), "QI_VAULT_PATH="+cfg.VaultPath)

	if lc.Detach {
		c := exec.Command(bin, argv...)
		c.Dir = cfg.VaultPath
		c.Env = env
		if err := c.Start(); err != nil {
			return fmt.Errorf("launch: starting %q: %w", lc.Harness, err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Launched %s (pid %d).\n", lc.Harness, c.Process.Pid)
		return c.Process.Release()
	}

	return execReplace(bin, argv, env, cfg.VaultPath)
}
