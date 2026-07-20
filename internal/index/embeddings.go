package index

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"qi/internal/domain"
)

// semanticPreviewLen caps the body preview shown for a semantic hit. There is no
// text query to anchor a snippet on, so we show the start of the note.
const semanticPreviewLen = 160

// NoteEmbedding is one stored vector keyed by note path.
type NoteEmbedding struct {
	Path   string
	Vector []float32
}

// encodeVector serializes a float32 slice to little-endian bytes (4 bytes each)
// for BLOB storage.
func encodeVector(vec []float32) []byte {
	buf := make([]byte, len(vec)*4)
	for i, f := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// decodeVector is the inverse of encodeVector. A trailing partial float (len not
// a multiple of 4) is ignored.
func decodeVector(buf []byte) []float32 {
	vec := make([]float32, len(buf)/4)
	for i := range vec {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
	}
	return vec
}

// cosine returns the cosine similarity of a and b in [-1, 1]. Callers must pass
// equal-length vectors (SemanticSearch rejects a dim mismatch up front rather
// than let a stale-model vector rank as garbage); the shorter-prefix guard here
// is a defensive backstop, and a zero-norm vector yields 0 (no direction).
func cosine(a, b []float32) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var dot, na, nb float64
	for i := 0; i < n; i++ {
		av, bv := float64(a[i]), float64(b[i])
		dot += av * bv
		na += av * av
		nb += bv * bv
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// UpsertEmbedding stores (or replaces) the vector for path under model.
func (idx *Indexer) UpsertEmbedding(path, model string, vec []float32) error {
	// Nano precision (parity with the FTS last_indexed marker) so doctor's
	// newest-note-mtime-vs-embedding compare doesn't misjudge same-second edits.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := idx.db.Exec(`
		INSERT INTO note_embeddings(path, model, dim, vector, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			model      = excluded.model,
			dim        = excluded.dim,
			vector     = excluded.vector,
			updated_at = excluded.updated_at
	`, path, model, len(vec), encodeVector(vec), now)
	if err != nil {
		return fmt.Errorf("upsert embedding %s: %w", path, err)
	}
	return nil
}

// ClearEmbeddings removes all stored vectors. Called before a full `qi embed`
// rebuild so removed/renamed notes don't linger.
func (idx *Indexer) ClearEmbeddings() error {
	if _, err := idx.db.Exec("DELETE FROM note_embeddings"); err != nil {
		return fmt.Errorf("clear embeddings: %w", err)
	}
	return nil
}

// EmbeddingStats reports the stored semantic-vector count, the newest vector
// updated_at, and the distinct models present — opened read-only (like Stats)
// so `qi doctor` stays genuinely mutation-free. A missing db or a missing
// note_embeddings table (semantic search never used) comes back as zeros, which
// the caller treats as "not built", not an error.
func EmbeddingStats() (count int, newest time.Time, models []string, err error) {
	db, err := sql.Open("sqlite", "file:"+dbPath()+"?mode=ro")
	if err != nil {
		return 0, time.Time{}, nil, fmt.Errorf("open db read-only: %w", err)
	}
	defer db.Close()

	var raw sql.NullString
	switch err := db.QueryRow("SELECT COUNT(*), MAX(updated_at) FROM note_embeddings").Scan(&count, &raw); {
	case err == nil:
		if raw.Valid {
			if t, perr := time.Parse(time.RFC3339Nano, raw.String); perr == nil {
				newest = t
			}
		}
	case strings.Contains(err.Error(), "no such table"):
		// legacy db, or semantic search never used — leave zeros
		return 0, time.Time{}, nil, nil
	default:
		return 0, time.Time{}, nil, fmt.Errorf("read embedding stats: %w", err)
	}

	rows, err := db.Query("SELECT DISTINCT model FROM note_embeddings ORDER BY model")
	if err != nil {
		return count, newest, nil, fmt.Errorf("query embedding models: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return count, newest, models, fmt.Errorf("scan model: %w", err)
		}
		models = append(models, m)
	}
	return count, newest, models, rows.Err()
}

// EmbeddingsFor loads all stored vectors for the given model.
func (idx *Indexer) EmbeddingsFor(model string) ([]NoteEmbedding, error) {
	rows, err := idx.db.Query("SELECT path, vector FROM note_embeddings WHERE model = ?", model)
	if err != nil {
		return nil, fmt.Errorf("query embeddings: %w", err)
	}
	defer rows.Close()

	var out []NoteEmbedding
	for rows.Next() {
		var path string
		var blob []byte
		if err := rows.Scan(&path, &blob); err != nil {
			return nil, fmt.Errorf("scan embedding: %w", err)
		}
		out = append(out, NoteEmbedding{Path: path, Vector: decodeVector(blob)})
	}
	return out, rows.Err()
}

// SemanticSearch cosine-ranks the stored vectors for model against query and
// returns the top results, each labelled by kind (derived from its path) and
// carrying a best-effort body preview pulled from the FTS notes table. An empty
// store returns no results (the caller should hint `qi embed`).
func (idx *Indexer) SemanticSearch(model string, query []float32, opts SearchOptions) ([]domain.SearchResult, error) {
	embeddings, err := idx.EmbeddingsFor(model)
	if err != nil {
		return nil, err
	}
	if len(embeddings) == 0 {
		return nil, nil
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}

	var keep map[string]bool
	if len(opts.Kinds) > 0 {
		keep = make(map[string]bool, len(opts.Kinds))
		for _, k := range opts.Kinds {
			keep[k] = true
		}
	}

	type scored struct {
		path  string
		kind  string
		score float64
	}
	qdim := len(query)
	ranked := make([]scored, 0, len(embeddings))
	for _, e := range embeddings {
		// Reject a dimension mismatch loudly instead of cosine-comparing the
		// shorter prefix (which silently returns garbage rankings). The realistic
		// cause is the embed model being changed without re-running `qi embed`;
		// stored vectors and the freshly-embedded query then differ in length.
		if len(e.Vector) != qdim {
			return nil, fmt.Errorf("semantic index dimension mismatch (%s: stored %d, query %d): the embedding model changed since `qi embed` — run `qi embed` to rebuild", e.Path, len(e.Vector), qdim)
		}
		kind := classifyKind(e.Path)
		if keep != nil && !keep[kind] {
			continue
		}
		ranked = append(ranked, scored{path: e.Path, kind: kind, score: cosine(e.Vector, query)})
	}

	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].score > ranked[j].score
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}

	results := make([]domain.SearchResult, 0, len(ranked))
	for _, r := range ranked {
		title, preview := idx.previewFor(r.path)
		results = append(results, domain.SearchResult{
			Note: domain.Note{
				Path:  r.path,
				Title: title,
			},
			Match: preview,
			Rank:  r.score,
			Kind:  r.kind,
		})
	}
	return results, nil
}

// previewFor fetches the title and a leading body preview for path from the FTS
// notes table. Best-effort: a path not yet in the FTS index returns empties.
func (idx *Indexer) previewFor(path string) (title, preview string) {
	var body string
	err := idx.db.QueryRow("SELECT title, body FROM notes WHERE path = ?", path).Scan(&title, &body)
	if err != nil {
		return "", ""
	}
	preview = firstChars(body, semanticPreviewLen)
	return title, preview
}

// firstChars returns the first n runes of s (trimmed), appending an ellipsis
// when truncated. Newlines are flattened so the preview stays on one line.
func firstChars(s string, n int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	truncated := false
	if len(runes) > n {
		runes = runes[:n]
		truncated = true
	}
	out := strings.ReplaceAll(string(runes), "\n", " ")
	if truncated {
		out += "..."
	}
	return out
}
