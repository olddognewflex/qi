package commands

import (
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
	configCmd.AddCommand(newConfigEditCommand())
	return configCmd
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
