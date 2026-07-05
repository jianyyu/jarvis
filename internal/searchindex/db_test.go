package searchindex

import (
	"path/filepath"
	"testing"
)

func TestOpenCreatesSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	idx, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer idx.Close()

	// Both tables must exist and be queryable.
	if _, err := idx.db.Exec(`INSERT INTO sessions_fts(jarvis_id, name) VALUES ('x', 'hello')`); err != nil {
		t.Fatalf("sessions_fts not usable: %v", err)
	}
	if _, err := idx.db.Exec(`INSERT INTO index_meta(jarvis_id, rowid_ref, transcript_path, indexed_mtime, ai_title) VALUES ('x', 1, '/p', 0, '')`); err != nil {
		t.Fatalf("index_meta not usable: %v", err)
	}

	// Reopening the same path must not error (idempotent migration).
	idx2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	idx2.Close()
}
