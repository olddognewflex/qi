package index

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
	"qi/internal/domain"
	"qi/internal/vault"
)

type Indexer struct {
	db *sql.DB
}

func dbPath() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "qi", "qi.db")
}

// DBPath returns the absolute path to the SQLite index database, so callers
// (e.g. `qi doctor`) can stat it without opening the DB.
func DBPath() string {
	return dbPath()
}

func ensureDir(path string) error {
	return os.MkdirAll(filepath.Dir(path), 0o755)
}

func Open() (*Indexer, error) {
	path := dbPath()
	if err := ensureDir(path); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := initSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &Indexer{db: db}, nil
}

func initSchema(db *sql.DB) error {
	if _, err := db.Exec(`
		CREATE VIRTUAL TABLE IF NOT EXISTS notes USING fts5(
			title,
			body,
			path UNINDEXED,
			tokenize='porter'
		);
	`); err != nil {
		return err
	}
	// task_sync_state is the 3-way-merge ancestor for cross-vault task sync.
	// DERIVED state (invariant #4): fully rebuildable by re-seeding from the
	// canonical markdown task lines, never the source of truth.
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS task_sync_state (
			id        TEXT PRIMARY KEY,
			project   TEXT NOT NULL,
			base_line TEXT NOT NULL,
			synced_at TEXT NOT NULL
		);
	`); err != nil {
		return err
	}
	// note_embeddings stores per-note semantic vectors for opt-in `qi search
	// --semantic`. DERIVED state (invariant #1): fully rebuildable from the
	// markdown via `qi embed`, never the source of truth.
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS note_embeddings (
			path       TEXT PRIMARY KEY,
			model      TEXT NOT NULL,
			dim        INTEGER NOT NULL,
			vector     BLOB NOT NULL,
			updated_at TEXT NOT NULL
		);
	`)
	return err
}

// SyncBase is one row of the task_sync_state ancestor table: the canonical
// FormatTaskLine output (base_line) and owning project at the last successful
// sync. synced_at is not surfaced here — it is bookkeeping written on commit.
type SyncBase struct {
	ID       string
	Project  string
	BaseLine string
}

// LoadSyncState returns the current sync ancestor keyed by task id.
func (idx *Indexer) LoadSyncState() (map[string]SyncBase, error) {
	rows, err := idx.db.Query("SELECT id, project, base_line FROM task_sync_state")
	if err != nil {
		return nil, fmt.Errorf("query sync state: %w", err)
	}
	defer rows.Close()

	out := make(map[string]SyncBase)
	for rows.Next() {
		var b SyncBase
		if err := rows.Scan(&b.ID, &b.Project, &b.BaseLine); err != nil {
			return nil, fmt.Errorf("scan sync state: %w", err)
		}
		out[b.ID] = b
	}
	return out, rows.Err()
}

// CommitSyncState applies the given upserts and deletes to task_sync_state in a
// single transaction. It is the LAST step of a reconcile: committing only after
// all file writes succeed means a crash mid-write leaves base stale and the next
// run self-heals (invariant #4 / partial-write recovery).
func (idx *Indexer) CommitSyncState(upserts []SyncBase, deletes []string) error {
	tx, err := idx.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if len(upserts) > 0 {
		now := time.Now().UTC().Format(time.RFC3339)
		stmt, err := tx.Prepare(`
			INSERT INTO task_sync_state(id, project, base_line, synced_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				project   = excluded.project,
				base_line = excluded.base_line,
				synced_at = excluded.synced_at
		`)
		if err != nil {
			return fmt.Errorf("prepare upsert: %w", err)
		}
		defer stmt.Close()
		for _, u := range upserts {
			if _, err := stmt.Exec(u.ID, u.Project, u.BaseLine, now); err != nil {
				return fmt.Errorf("upsert sync state %s: %w", u.ID, err)
			}
		}
	}

	if len(deletes) > 0 {
		del, err := tx.Prepare("DELETE FROM task_sync_state WHERE id = ?")
		if err != nil {
			return fmt.Errorf("prepare delete: %w", err)
		}
		defer del.Close()
		for _, id := range deletes {
			if _, err := del.Exec(id); err != nil {
				return fmt.Errorf("delete sync state %s: %w", id, err)
			}
		}
	}

	return tx.Commit()
}

func (idx *Indexer) Close() error {
	return idx.db.Close()
}

func (idx *Indexer) Rebuild(vaultPath string) error {
	tx, err := idx.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM notes"); err != nil {
		return fmt.Errorf("clear notes: %w", err)
	}

	stmt, err := tx.Prepare("INSERT INTO notes(title, body, path) VALUES (?, ?, ?)")
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()

	err = vault.WalkVault(vaultPath, func(path, content string) error {
		title := extractFirstHeading(content)
		if title == "" {
			title = filepath.Base(path)
		}
		_, err := stmt.Exec(title, content, path)
		return err
	})
	if err != nil {
		return fmt.Errorf("walk vault: %w", err)
	}

	return tx.Commit()
}

// defaultSearchLimit is the user-facing result cap when none is requested,
// preserving the historical behavior of Search (LIMIT 50).
const defaultSearchLimit = 50

// SearchOptions tunes a SearchWith query. The zero value reproduces Search:
// all kinds, default limit.
type SearchOptions struct {
	// Kinds restricts results to these kinds (domain.SearchKind* values).
	// Empty means no filter (all kinds).
	Kinds []string
	// Limit caps the number of returned results. <= 0 means defaultSearchLimit.
	Limit int
}

// classifyKind derives a SearchResult kind from a result's path by matching the
// vault subdir it lives under. Matching is segment-based (on path separators)
// so it works for both absolute vault paths and relative paths, and won't false-
// match a directory name embedded in a longer segment.
func classifyKind(path string) string {
	for _, seg := range strings.Split(filepath.ToSlash(path), "/") {
		switch seg {
		case "00-inbox":
			return domain.SearchKindInbox
		case "10-tasks":
			return domain.SearchKindTask
		case "20-notes":
			return domain.SearchKindNote
		case "30-daily":
			return domain.SearchKindDaily
		}
	}
	return domain.SearchKindOther
}

// Search runs an FTS query across all vault markdown, returning up to 50 ranked
// results of any kind. Behavior-preserving wrapper over SearchWith; results
// carry a populated Kind.
func (idx *Indexer) Search(query string) ([]domain.SearchResult, error) {
	return idx.SearchWith(query, SearchOptions{})
}

// SearchWith runs an FTS query and labels each hit by kind (derived from its
// path). When opts.Kinds is set, results are filtered to those kinds in Go
// (the kind is not a stored column), so a wider SQL window is fetched to still
// fill the user limit after filtering.
func (idx *Indexer) SearchWith(query string, opts SearchOptions) ([]domain.SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("empty query")
	}

	userLimit := opts.Limit
	if userLimit <= 0 {
		userLimit = defaultSearchLimit
	}

	// When filtering by kind, over-fetch so post-filter still fills userLimit.
	sqlLimit := userLimit
	if len(opts.Kinds) > 0 {
		sqlLimit = max(userLimit, defaultSearchLimit) * 5
		if sqlLimit > 1000 {
			sqlLimit = 1000
		}
	}

	rows, err := idx.db.Query(`
		SELECT title, body, path, rank
		FROM notes
		WHERE notes MATCH ?
		ORDER BY rank
		LIMIT ?
	`, query, sqlLimit)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()

	var keep map[string]bool
	if len(opts.Kinds) > 0 {
		keep = make(map[string]bool, len(opts.Kinds))
		for _, k := range opts.Kinds {
			keep[k] = true
		}
	}

	var results []domain.SearchResult
	for rows.Next() {
		var title, body, path string
		var rank float64
		if err := rows.Scan(&title, &body, &path, &rank); err != nil {
			continue
		}
		kind := classifyKind(path)
		if keep != nil && !keep[kind] {
			continue
		}
		snippet := extractSnippet(body, query)
		results = append(results, domain.SearchResult{
			Note: domain.Note{
				Path:  path,
				Title: title,
			},
			Match: snippet,
			Rank:  rank,
			Kind:  kind,
		})
		if len(results) >= userLimit {
			break
		}
	}
	return results, rows.Err()
}

func extractFirstHeading(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}

func extractSnippet(body, query string) string {
	lowerBody := strings.ToLower(body)
	lowerQuery := strings.ToLower(query)
	idx := strings.Index(lowerBody, lowerQuery)
	if idx == -1 {
		words := strings.Fields(lowerQuery)
		for _, w := range words {
			idx = strings.Index(lowerBody, w)
			if idx != -1 {
				break
			}
		}
	}
	if idx == -1 {
		if len(body) > 200 {
			return body[:200] + "..."
		}
		return body
	}

	start := idx - 60
	if start < 0 {
		start = 0
	}
	end := idx + len(query) + 60
	if end > len(body) {
		end = len(body)
	}
	snippet := body[start:end]
	if start > 0 {
		snippet = "..." + snippet
	}
	if end < len(body) {
		snippet = snippet + "..."
	}
	return strings.ReplaceAll(snippet, "\n", " ")
}
