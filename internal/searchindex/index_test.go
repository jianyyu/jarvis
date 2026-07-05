package searchindex

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"jarvis/internal/model"
	"jarvis/internal/store"
)

// seedSession writes a session.yaml under a temp JARVIS_HOME and a transcript
// JSONL under a fake ~/.claude/projects dir, returning the transcript path.
func seedSession(t *testing.T, id, name, launchDir, claudeID, jsonl string) string {
	t.Helper()
	s := &model.Session{
		ID: id, Type: "session", Name: name, Status: model.StatusActive,
		LaunchDir: launchDir, ClaudeSessionID: claudeID,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := store.SaveSession(s); err != nil {
		t.Fatalf("save session: %v", err)
	}
	// Transcript lives at <home>/.claude/projects/<encode(launchDir)>/<claudeID>.jsonl
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".claude", "projects", encodeForTest(launchDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir projects: %v", err)
	}
	p := filepath.Join(dir, claudeID+".jsonl")
	if err := os.WriteFile(p, []byte(jsonl), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	t.Cleanup(func() { os.Remove(p) })
	return p
}

func encodeForTest(cwd string) string {
	// mirror paths.EncodeCWD
	out := make([]rune, 0, len(cwd))
	for _, r := range cwd {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			out = append(out, r)
		} else {
			out = append(out, '-')
		}
	}
	return string(out)
}

func TestSyncIndexesAndIsIncremental(t *testing.T) {
	t.Setenv("JARVIS_HOME", t.TempDir())
	launch := t.TempDir()
	seedSession(t, "sess1", "Deadlock fix", launch, "claude-1",
		`{"type":"user","message":{"role":"user","content":"the socket hangs on detach forever"}}`)

	idx, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer idx.Close()

	n, err := idx.Sync()
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if n != 1 {
		t.Fatalf("first sync indexed %d sessions, want 1", n)
	}

	// Second sync with no file change must reindex nothing.
	n, err = idx.Sync()
	if err != nil {
		t.Fatalf("sync 2: %v", err)
	}
	if n != 0 {
		t.Fatalf("second sync reindexed %d, want 0 (incremental)", n)
	}
}

func TestSyncRemovesDeletedSession(t *testing.T) {
	t.Setenv("JARVIS_HOME", t.TempDir())
	launch := t.TempDir()
	seedSession(t, "sess1", "Deadlock fix", launch, "claude-1",
		`{"type":"user","message":{"role":"user","content":"the socket hangs on detach forever"}}`)

	idx, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer idx.Close()
	if _, err := idx.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}

	if err := store.DeleteSession("sess1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := idx.Sync(); err != nil {
		t.Fatalf("sync 2: %v", err)
	}

	var count int
	idx.db.QueryRow(`SELECT count(*) FROM index_meta`).Scan(&count)
	if count != 0 {
		t.Fatalf("index_meta still has %d rows after session delete", count)
	}
}
