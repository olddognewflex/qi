package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"qi/internal/config"
	"qi/internal/embed"
	"qi/internal/index"
	"qi/internal/vault"
)

// embedPerNoteTimeout bounds a single Ollama /api/embed call so one slow note
// can't stall the whole build.
const embedPerNoteTimeout = 30 * time.Second

func newEmbedCommand(cfg config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "embed",
		Short: "Build local embeddings for semantic search (opt-in)",
		Long: "Embed every vault note via Ollama and store the vectors in the local\n" +
			"SQLite index for `qi search --semantic`. Derived state — fully rebuildable.\n" +
			"Requires [embeddings] enabled = true in config.toml.\n\n" +
			"This is a full rebuild. Unlike the FTS index, embeddings are NOT kept\n" +
			"fresh incrementally: editing a note leaves its vector stale until you\n" +
			"re-run `qi embed`. `qi doctor` reports that staleness (and any embed-model\n" +
			"change) so the drift is visible.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cfg.Embeddings.Enabled {
				return fmt.Errorf("embeddings are disabled; set [embeddings] enabled = true in config.toml")
			}

			embedder := embed.NewOllamaEmbedder(cfg.Embeddings.OllamaURL, cfg.Embeddings.Model, nil)

			idx, err := index.Open()
			if err != nil {
				return fmt.Errorf("open index: %w", err)
			}
			defer idx.Close()

			if err := idx.ClearEmbeddings(); err != nil {
				return fmt.Errorf("clear embeddings: %w", err)
			}

			start := time.Now()
			count := 0
			walkErr := vault.WalkVault(cfg.VaultPath, func(path, content string) error {
				if strings.TrimSpace(content) == "" {
					return nil
				}
				ctx, cancel := context.WithTimeout(cmd.Context(), embedPerNoteTimeout)
				vec, err := embedder.Embed(ctx, content)
				cancel()
				if err != nil {
					// One bad file shouldn't abort the whole run; warn and continue.
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: embed %s: %v\n", path, err)
					return nil
				}
				if err := idx.UpsertEmbedding(path, embedder.Model(), vec); err != nil {
					return err
				}
				count++
				return nil
			})
			if walkErr != nil {
				return fmt.Errorf("embed vault: %w", walkErr)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Embedded %d note(s) in %s\n", count, time.Since(start).Round(time.Millisecond))
			return nil
		},
	}
}
