package searchindex

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
// schema exists. WAL journaling and a per-connection busy_timeout are applied
// via the DSN so every pooled connection gets them at connect time — this lets
// background Sync writes coexist with Search reads without instant SQLITE_BUSY
// failures.
func Open(path string) (*Index, error) {
	// The path is embedded in a DSN; modernc's driver truncates the filename
	// at the first '?' or '#' and would silently write the DB elsewhere.
	if strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("index path must not contain '?' or '#': %q", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir index dir: %w", err)
	}
	dsn := path + "?_pragma=busy_timeout(3000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	idx := &Index{db: db}
	if err := idx.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return idx, nil
}

// migrate creates the schema if absent. Note: CREATE ... IF NOT EXISTS
// silently no-ops on a pre-existing table with a stale column set, so any
// future column change to sessions_fts or index_meta requires a filename bump
// (e.g. index_v2.db) or an explicit DROP/recreate. The spec (§9) sanctions
// deleting the index file to rebuild from transcripts.
// Schema finalized pre-release; any dev index.db created from intermediate
// commits should be deleted (spec §9).
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
			ai_title        TEXT,
			name            TEXT
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
