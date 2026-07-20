package commands

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"qi/internal/config"
)

// newInitCommand creates `qi init`: writes a commented starter config.toml to
// the standard location. It is a thin handler — the template, path resolution,
// and the write live in internal/config. Runnable on a fresh install (marked
// skip-config in root.go).
func newInitCommand() *cobra.Command {
	var vaultFlag string
	var force bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create a starter config.toml",
		Long: "Writes a commented starter config.toml to the standard location\n" +
			"(honoring XDG_CONFIG_HOME, default ~/.config/qi/config.toml).\n\n" +
			"Prompts for a vault path unless --vault is given or stdin is not a\n" +
			"terminal, in which case the default is used. Refuses to overwrite an\n" +
			"existing config unless --force is passed.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := config.ConfigPath()
			if _, err := os.Stat(path); err == nil && !force {
				return fmt.Errorf("config already exists at %s\nedit it with `qi config edit`, or pass --force to overwrite", path)
			}

			vault := strings.TrimSpace(vaultFlag)
			if vault == "" {
				vault = promptVaultPath(cmd, config.DefaultVaultPath())
			}

			written, err := config.WriteStarterConfig(vault, force)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Wrote starter config to %s\n", written)
			fmt.Fprintf(out, "vault_path set to %s\n", config.ExpandPath(vault))
			fmt.Fprintln(out, "Next: create that directory, then run `qi doctor` to verify your setup.")
			return nil
		},
	}

	cmd.Flags().StringVar(&vaultFlag, "vault", "", "vault path (skips the interactive prompt)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing config")
	return cmd
}

// promptVaultPath asks for a vault path, defaulting to def. A blank line (or a
// non-interactive/closed stdin) accepts the default, so `qi init` works when
// driven non-interactively (piped or empty stdin).
func promptVaultPath(cmd *cobra.Command, def string) string {
	fmt.Fprintf(cmd.OutOrStdout(), "Vault path [%s]: ", def)
	reader := bufio.NewReader(cmd.InOrStdin())
	line, _ := reader.ReadString('\n')
	entered := strings.TrimSpace(line)
	if entered == "" {
		return def
	}
	return entered
}
