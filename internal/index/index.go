package index

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	_, err := db.Exec(`
		CREATE VIRTUAL TABLE IF NOT EXISTS notes USING fts5(
			title,
			body,
			path UNINDEXED,
			tokenize='porter'
		);
	`)
	return err
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

func (idx *Indexer) Search(query string) ([]domain.SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("empty query")
	}

	rows, err := idx.db.Query(`
		SELECT title, body, path, rank
		FROM notes
		WHERE notes MATCH ?
		ORDER BY rank
		LIMIT 50
	`, query)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()

	var results []domain.SearchResult
	for rows.Next() {
		var title, body, path string
		var rank float64
		if err := rows.Scan(&title, &body, &path, &rank); err != nil {
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
		})
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
