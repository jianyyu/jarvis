package searchindex

import (
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// TestFTS5AndSnippetAvailable is the go/no-go gate: the whole design depends on
// FTS5 being compiled into the driver and snippet(-1) auto-selecting the
// best-matching column.
func TestFTS5AndSnippetAvailable(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE VIRTUAL TABLE t USING fts5(a, b, tokenize='trigram')`)
	if err != nil {
		t.Fatalf("FTS5 not available in this driver build: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO t(a, b) VALUES ('hello world', 'goodbye planet')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var snip string
	err = db.QueryRow(
		`SELECT snippet(t, -1, '[', ']', '…', 8) FROM t WHERE t MATCH '"planet"'`,
	).Scan(&snip)
	if err != nil {
		t.Fatalf("snippet(-1) query failed: %v", err)
	}
	// -1 must pick column b (where "planet" is), not column a.
	if !strings.Contains(snip, "[planet]") {
		t.Fatalf("snippet(-1) did not highlight the match in the right column: %q", snip)
	}
}
