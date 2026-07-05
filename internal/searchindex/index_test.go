package searchindex

import (
	"os"
	"path/filepath"
	"strings"
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

func TestSyncUpdatesRenamedSession(t *testing.T) {
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

	// Rename the session in the store without touching the transcript.
	s, err := store.GetSession("sess1")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	s.Name = "Marketplace socket fix"
	if err := store.SaveSession(s); err != nil {
		t.Fatalf("save renamed session: %v", err)
	}

	n, err := idx.Sync()
	if err != nil {
		t.Fatalf("sync 2: %v", err)
	}
	if n != 1 {
		t.Fatalf("sync after rename reindexed %d, want 1", n)
	}

	var name, metadata string
	if err := idx.db.QueryRow(`SELECT name, metadata FROM sessions_fts`).Scan(&name, &metadata); err != nil {
		t.Fatalf("query fts row: %v", err)
	}
	if name != "Marketplace socket fix" {
		t.Fatalf("fts name = %q, want new name", name)
	}
	if !strings.Contains(metadata, "Marketplace socket fix") {
		t.Fatalf("fts metadata %q does not contain new name", metadata)
	}

	var count int
	idx.db.QueryRow(`SELECT count(*) FROM sessions_fts`).Scan(&count)
	if count != 1 {
		t.Fatalf("sessions_fts has %d rows after rename, want 1", count)
	}
}

func TestSyncUsesStoredPathWhenLaunchDirMoves(t *testing.T) {
	t.Setenv("JARVIS_HOME", t.TempDir())
	launch := t.TempDir()
	p := seedSession(t, "sess1", "Deadlock fix", launch, "claude-1",
		`{"type":"user","message":{"role":"user","content":"the socket hangs on detach forever"}}`)

	idx, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer idx.Close()
	if _, err := idx.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// Point the session at a LaunchDir with no transcript. ProjectDirs-based
	// lookup would now find nothing, but the transcript still lives at the
	// stored path — Sync must use the stored path (stat-first) and pick up
	// the changed content.
	s, err := store.GetSession("sess1")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	s.LaunchDir = t.TempDir()
	if err := store.SaveSession(s); err != nil {
		t.Fatalf("save session: %v", err)
	}
	if err := os.WriteFile(p, []byte(
		`{"type":"user","message":{"role":"user","content":"now the marketplace listing times out"}}`), 0o644); err != nil {
		t.Fatalf("rewrite transcript: %v", err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	n, err := idx.Sync()
	if err != nil {
		t.Fatalf("sync 2: %v", err)
	}
	if n != 1 {
		t.Fatalf("sync after content change reindexed %d, want 1 (stored path not used?)", n)
	}
	var userText string
	if err := idx.db.QueryRow(`SELECT user_text FROM sessions_fts`).Scan(&userText); err != nil {
		t.Fatalf("query fts row: %v", err)
	}
	if !strings.Contains(userText, "marketplace listing times out") {
		t.Fatalf("user_text %q missing updated content", userText)
	}
}

func TestSyncFallsBackWhenStoredPathStale(t *testing.T) {
	t.Setenv("JARVIS_HOME", t.TempDir())
	launch := t.TempDir()
	p := seedSession(t, "sess1", "Deadlock fix", launch, "claude-1",
		`{"type":"user","message":{"role":"user","content":"the socket hangs on detach forever"}}`)

	idx, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer idx.Close()
	if _, err := idx.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// Move the transcript to the session's (new) worktree project dir so the
	// stored path stats fail and Sync must recover via paths.ProjectDirs.
	wt := t.TempDir()
	s, err := store.GetSession("sess1")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	s.WorktreeDir = wt
	if err := store.SaveSession(s); err != nil {
		t.Fatalf("save session: %v", err)
	}
	home, _ := os.UserHomeDir()
	newDir := filepath.Join(home, ".claude", "projects", encodeForTest(wt))
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatalf("mkdir projects: %v", err)
	}
	newPath := filepath.Join(newDir, "claude-1.jsonl")
	if err := os.Rename(p, newPath); err != nil {
		t.Fatalf("move transcript: %v", err)
	}
	t.Cleanup(func() { os.Remove(newPath) })
	if err := os.WriteFile(newPath, []byte(
		`{"type":"user","message":{"role":"user","content":"now the marketplace listing times out"}}`), 0o644); err != nil {
		t.Fatalf("rewrite transcript: %v", err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(newPath, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	n, err := idx.Sync()
	if err != nil {
		t.Fatalf("sync 2: %v", err)
	}
	if n != 1 {
		t.Fatalf("sync after move reindexed %d, want 1 (fallback broken?)", n)
	}
	var storedPath string
	if err := idx.db.QueryRow(`SELECT transcript_path FROM index_meta WHERE jarvis_id='sess1'`).Scan(&storedPath); err != nil {
		t.Fatalf("query index_meta: %v", err)
	}
	if storedPath != newPath {
		t.Fatalf("index_meta.transcript_path = %q, want %q", storedPath, newPath)
	}
	var userText string
	if err := idx.db.QueryRow(`SELECT user_text FROM sessions_fts`).Scan(&userText); err != nil {
		t.Fatalf("query fts row: %v", err)
	}
	if !strings.Contains(userText, "marketplace listing times out") {
		t.Fatalf("user_text %q missing updated content", userText)
	}
}

func TestSyncRenameAndTranscriptChangeTogether(t *testing.T) {
	t.Setenv("JARVIS_HOME", t.TempDir())
	launch := t.TempDir()
	p := seedSession(t, "sess1", "Deadlock fix", launch, "claude-1",
		`{"type":"user","message":{"role":"user","content":"the socket hangs on detach forever"}}`)

	idx, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer idx.Close()
	if _, err := idx.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// Rename AND change the transcript in the same interval: the full upsert
	// path must refresh the name everywhere along with the new content.
	s, err := store.GetSession("sess1")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	s.Name = "Marketplace socket fix"
	if err := store.SaveSession(s); err != nil {
		t.Fatalf("save renamed session: %v", err)
	}
	if err := os.WriteFile(p, []byte(
		`{"type":"user","message":{"role":"user","content":"now the marketplace listing times out"}}`), 0o644); err != nil {
		t.Fatalf("rewrite transcript: %v", err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	n, err := idx.Sync()
	if err != nil {
		t.Fatalf("sync 2: %v", err)
	}
	if n != 1 {
		t.Fatalf("sync reindexed %d, want 1", n)
	}

	var name, userText string
	if err := idx.db.QueryRow(`SELECT name, user_text FROM sessions_fts`).Scan(&name, &userText); err != nil {
		t.Fatalf("query fts row: %v", err)
	}
	if name != "Marketplace socket fix" {
		t.Fatalf("fts name = %q, want new name", name)
	}
	if !strings.Contains(userText, "marketplace listing times out") {
		t.Fatalf("user_text %q missing updated content", userText)
	}
	var count int
	idx.db.QueryRow(`SELECT count(*) FROM sessions_fts`).Scan(&count)
	if count != 1 {
		t.Fatalf("sessions_fts has %d rows, want 1", count)
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
