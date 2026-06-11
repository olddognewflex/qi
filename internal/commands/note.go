package commands

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"qi/internal/config"
	"qi/internal/domain"
	"qi/internal/index"
	"qi/internal/service"
)

func newNoteCommand(cfg config.Config) *cobra.Command {
	noteSvc := service.NoteService{NotesDir: cfg.NotesPath}

	noteCmd := &cobra.Command{
		Use:   "note",
		Short: "Manage notes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := os.MkdirAll(cfg.InboxPath, 0o755); err != nil {
				return fmt.Errorf("create inbox dir: %w", err)
			}
			filename := fmt.Sprintf("untitled-%s.md", time.Now().Format("20060102-150405"))
			full := filepath.Join(cfg.InboxPath, filename)
			if err := os.WriteFile(full, []byte{}, 0o644); err != nil {
				return fmt.Errorf("create note: %w", err)
			}
			vaultName := filepath.Base(cfg.VaultPath)
			rel := "00-inbox/" + url.QueryEscape(filename)
			uri := "obsidian://open?vault=" + url.QueryEscape(vaultName) +
				"&file=" + rel
			return openURL(uri)
		},
	}

	var newProject string
	var newClient string
	newCmd := &cobra.Command{
		Use:   "new <title>",
		Short: "Create a new note",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, _ := cmd.Flags().GetString("body")

			// --client/--project route the note into that vault's configured
			// notes dir (notes_path, default 00-inbox); default is the main vault.
			vaultPath, notesDir, err := cfg.NoteVaultFor(newClient, newProject)
			if err != nil {
				return err
			}
			svc := service.NoteService{NotesDir: notesDir}
			note, err := svc.AddNote(args[0], body)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(vaultPath, note.Path)
			if err != nil {
				rel = note.Path
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Created %s\n", rel)
			return nil
		},
	}
	newCmd.Flags().StringP("body", "b", "", "initial note body")
	newCmd.Flags().StringVarP(&newProject, "project", "p", "", "write into the project vault's notes")
	newCmd.Flags().StringVarP(&newClient, "client", "c", "", "write into the client vault's notes")

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

			results, err := idx.SearchWith(args[0], index.SearchOptions{Kinds: []string{domain.SearchKindNote}})
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

func openURL(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	return cmd.Start()
}
