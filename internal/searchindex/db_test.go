package searchindex

import (
	"context"
	"database/sql"
	"os"
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

func TestOpenRejectsPathWithQueryChars(t *testing.T) {
	// modernc's driver splits the DSN at the first '?' (and '#'), so a path
	// containing those chars would silently truncate the filename and write
	// the DB to the wrong location. Open must reject such paths outright.
	root := t.TempDir()
	dbPath := filepath.Join(root, "weird?dir", "index.db")
	idx, err := Open(dbPath)
	if err == nil {
		idx.Close()
		t.Fatalf("open %q: expected error, got nil", dbPath)
	}

	// No stray files or directories may have been created.
	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		t.Fatalf("readdir: %v", readErr)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("stray files created: %v", names)
	}
}

func TestOpenAppliesPragmasToAllConnections(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	idx, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer idx.Close()

	ctx := context.Background()

	// Hold several distinct pooled connections open simultaneously so each
	// Conn call is forced to create a fresh connection. Every one of them
	// must have the pragmas applied, not just the first.
	const nConns = 4
	conns := make([]*sql.Conn, 0, nConns)
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()
	for i := 0; i < nConns; i++ {
		conn, err := idx.db.Conn(ctx)
		if err != nil {
			t.Fatalf("conn %d: %v", i, err)
		}
		conns = append(conns, conn)
	}

	for i, conn := range conns {
		var busyTimeout int
		if err := conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
			t.Fatalf("conn %d: query busy_timeout: %v", i, err)
		}
		if busyTimeout != 3000 {
			t.Errorf("conn %d: busy_timeout = %d, want 3000", i, busyTimeout)
		}

		// The journal_mode assertion is documentation-only: WAL persists in
		// the database file header, so this would pass even without the DSN
		// pragma. busy_timeout above is the load-bearing assertion.
		var journalMode string
		if err := conn.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
			t.Fatalf("conn %d: query journal_mode: %v", i, err)
		}
		if journalMode != "wal" {
			t.Errorf("conn %d: journal_mode = %q, want \"wal\"", i, journalMode)
		}
	}
}
