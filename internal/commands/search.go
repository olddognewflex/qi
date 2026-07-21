package commands

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"qi/internal/config"
	"qi/internal/domain"
	"qi/internal/embed"
	"qi/internal/index"
)

// validSearchKinds are the kind labels accepted by --kind, mapped to the domain
// consts the index classifier emits.
var validSearchKinds = map[string]bool{
	domain.SearchKindNote:  true,
	domain.SearchKindTask:  true,
	domain.SearchKindDaily: true,
	domain.SearchKindInbox: true,
	domain.SearchKindOther: true,
}

func newSearchCommand(cfg config.Config) *cobra.Command {
	var kinds []string
	var limit int
	var semantic bool
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search the whole vault (notes, tasks, dailies, inbox) via the FTS index",
		Long: "Full-text search across all vault markdown, labelling each hit by kind\n" +
			"(note / task / daily / inbox / other) derived from the vault subdir.\n" +
			"Filter with --kind and cap results with --limit. With --semantic, rank by\n" +
			"cosine similarity against local embeddings (requires `qi embed`).",
		Example: "  qi search \"quarterly plan\"\n" +
			"  qi search meeting --kind note --limit 5\n" +
			"  qi search \"how did I fix the socket bug\" --semantic\n" +
			"  qi search deploy --json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, k := range kinds {
				if !validSearchKinds[k] {
					return fmt.Errorf("unknown kind %q; valid kinds: note, task, daily, inbox, other", k)
				}
			}

			idx, err := index.Open()
			if err != nil {
				return fmt.Errorf("open index: %w", err)
			}
			defer idx.Close()

			var results []domain.SearchResult
			if semantic {
				if !cfg.Embeddings.Enabled {
					return fmt.Errorf("embeddings are disabled; set [embeddings] enabled = true in config.toml")
				}
				embedder := embed.NewOllamaEmbedder(cfg.Embeddings.OllamaURL, cfg.Embeddings.Model, nil)
				ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
				qvec, err := embedder.Embed(ctx, args[0])
				cancel()
				if err != nil {
					return fmt.Errorf("embed query: %w", err)
				}
				results, err = idx.SemanticSearch(cfg.Embeddings.Model, qvec, index.SearchOptions{Kinds: kinds, Limit: limit})
				if err != nil {
					return fmt.Errorf("semantic search: %w", err)
				}
				if len(results) == 0 && !asJSON {
					fmt.Fprintln(cmd.OutOrStdout(), "No matches. (Run `qi embed` to build the embeddings index.)")
					return nil
				}
			} else {
				results, err = idx.SearchWith(args[0], index.SearchOptions{Kinds: kinds, Limit: limit})
				if err != nil {
					return fmt.Errorf("search: %w", err)
				}
				if len(results) == 0 && !asJSON {
					fmt.Fprintln(cmd.OutOrStdout(), "No matches.")
					return nil
				}
			}

			// Path relative to the vault root, matching the human output and
			// keeping the JSON schema vault-relative (not machine-absolute).
			toRel := func(p string) string {
				rel, err := filepath.Rel(cfg.VaultPath, p)
				if err != nil {
					return p
				}
				return rel
			}

			if asJSON {
				out := make([]searchResultJSON, 0, len(results))
				for _, r := range results {
					out = append(out, searchResultJSON{
						Kind:  r.Kind,
						Path:  toRel(r.Note.Path),
						Match: strings.TrimSpace(r.Match),
						Rank:  r.Rank,
					})
				}
				return printJSON(cmd, out)
			}

			for _, r := range results {
				rel := toRel(r.Note.Path)
				if semantic {
					fmt.Fprintf(cmd.OutOrStdout(), "[%s]\t%s  (%.2f)\n  %s\n\n", r.Kind, rel, r.Rank, strings.TrimSpace(r.Match))
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "[%s]\t%s\n  %s\n\n", r.Kind, rel, strings.TrimSpace(r.Match))
				}
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVarP(&kinds, "kind", "k", nil, "limit to kinds (comma-separated): note,task,daily,inbox,other")
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "max results")
	cmd.Flags().BoolVarP(&semantic, "semantic", "s", false, "rank by embedding cosine similarity (requires `qi embed`)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output as JSON (stable schema for scripts/agents)")
	return cmd
}
