package searchindex

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"jarvis/internal/store"

	_ "modernc.org/sqlite"
)

// Index is a handle to the SQLite FTS5 search index.
type Index struct {
	db *sql.DB
}

// DefaultPath returns the standard index location under JARVIS_HOME.
func DefaultPath() string {
	return filepath.Join(store.JarvisHome(), "search", "index.db")
}

// Open opens (creating if needed) the index database at path and ensures the
// schema exists. Uses WAL so background Sync writes don't block Search reads.
func Open(path string) (*Index, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir index dir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=3000;`); err != nil {
		db.Close()
		return nil, fmt.Errorf("pragma: %w", err)
	}
	idx := &Index{db: db}
	if err := idx.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return idx, nil
}

func (i *Index) migrate() error {
	stmts := []string{
		`CREATE VIRTUAL TABLE IF NOT EXISTS sessions_fts USING fts5(
			jarvis_id UNINDEXED,
			name,
			initial_prompt,
			user_text,
			assistant_text,
			metadata,
			tokenize='trigram'
		)`,
		`CREATE TABLE IF NOT EXISTS index_meta(
			jarvis_id       TEXT PRIMARY KEY,
			rowid_ref       INTEGER NOT NULL,
			transcript_path TEXT,
			indexed_mtime   INTEGER,
			ai_title        TEXT
		)`,
	}
	for _, s := range stmts {
		if _, err := i.db.Exec(s); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

// Close releases the database handle.
func (i *Index) Close() error {
	if i == nil || i.db == nil {
		return nil
	}
	return i.db.Close()
}
