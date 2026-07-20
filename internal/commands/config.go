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

func newConfigCommand() *cobra.Command {
	configCmd := &cobra.Command{
		Use:   "config",
		Short: "Manage qi configuration",
	}
	configCmd.AddCommand(newConfigEditCommand(), newConfigShowCommand())
	return configCmd
}

// newConfigShowCommand implements `qi config show`: the fully-resolved config
// as JSON, secrets redacted. It re-loads the config itself (rather than taking
// the value wired at construction) so a broken config surfaces the same clear
// load error here as everywhere else, instead of printing a hollow zero-value.
func newConfigShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the fully-resolved config as JSON (secrets redacted)",
		Long: "Resolve the config exactly as qi would at runtime — env overrides,\n" +
			"client/project flattening, derived paths — and print it as JSON with\n" +
			"secrets (passwords, tokens, client secrets, MCP env values) redacted.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			view := config.RedactView(cfg, config.ConfigPath())
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(view)
		},
	}
}

func newConfigEditCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "edit",
		Short: "Open the config file in $EDITOR",
		RunE: func(cmd *cobra.Command, args []string) error {
			editor := os.Getenv("EDITOR")
			if editor == "" {
				return fmt.Errorf("$EDITOR is not set")
			}

			path := config.ConfigPath()

			// Seed a commented starter template (not an empty file) the first
			// time, so a new user opens something they can actually fill in.
			if _, err := os.Stat(path); os.IsNotExist(err) {
				if _, err := config.WriteStarterConfig(config.DefaultVaultPath(), false); err != nil {
					return err
				}
			}

			parts := strings.Fields(editor)
			editorArgs := append(parts[1:], path)
			c := exec.Command(parts[0], editorArgs...)
			c.Stdin = os.Stdin
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr
			return c.Run()
		},
	}
}
