package searchindex

import (
	"context"
	"database/sql"
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

		var journalMode string
		if err := conn.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
			t.Fatalf("conn %d: query journal_mode: %v", i, err)
		}
		if journalMode != "wal" {
			t.Errorf("conn %d: journal_mode = %q, want \"wal\"", i, journalMode)
		}
	}
}
