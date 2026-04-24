package commands

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"qi/internal/config"
	"qi/internal/index"
	"qi/internal/service"
)

func newNoteCommand(cfg config.Config) *cobra.Command {
	noteSvc := service.NoteService{NotesDir: cfg.NotesPath}

	noteCmd := &cobra.Command{
		Use:   "note",
		Short: "Manage notes",
	}

	newCmd := &cobra.Command{
		Use:   "new <title>",
		Short: "Create a new note",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, _ := cmd.Flags().GetString("body")
			note, err := noteSvc.AddNote(args[0], body)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(cfg.VaultPath, note.Path)
			fmt.Fprintf(cmd.OutOrStdout(), "Created %s\n", rel)
			return nil
		},
	}
	newCmd.Flags().StringP("body", "b", "", "initial note body")

	searchCmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search notes via FTS index",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			idx, err := index.Open()
			if err != nil {
				return fmt.Errorf("open index: %w", err)
			}
			defer idx.Close()

			results, err := idx.Search(args[0])
			if err != nil {
				return fmt.Errorf("search: %w", err)
			}
			if len(results) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No matches.")
				return nil
			}
			for _, r := range results {
				rel, _ := filepath.Rel(cfg.VaultPath, r.Note.Path)
				fmt.Fprintf(cmd.OutOrStdout(), "%s\n  %s\n\n", rel, strings.TrimSpace(r.Match))
			}
			return nil
		},
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all notes",
		RunE: func(cmd *cobra.Command, args []string) error {
			notes, err := noteSvc.ListNotes()
			if err != nil {
				return err
			}
			if len(notes) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No notes.")
				return nil
			}
			for _, n := range notes {
				rel, _ := filepath.Rel(cfg.VaultPath, n.Path)
				fmt.Fprintf(cmd.OutOrStdout(), "%s\n", rel)
			}
			return nil
		},
	}

	noteCmd.AddCommand(newCmd, searchCmd, listCmd)
	return noteCmd
}
