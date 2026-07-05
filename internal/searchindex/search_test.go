package searchindex

import (
	"path/filepath"
	"strings"
	"testing"
)

func newSeededIndex(t *testing.T) *Index {
	t.Helper()
	t.Setenv("JARVIS_HOME", t.TempDir())
	launch := t.TempDir()

	seedSession(t, "sess-name", "PTY deadlock", launch, "c-name",
		`{"type":"user","message":{"role":"user","content":"just some unrelated chatter about lunch plans"}}`)
	seedSession(t, "sess-body", "random title", launch, "c-body",
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"The marketplace socket timeout happens because the retry backoff is too aggressive here."}]}}`)
	seedSession(t, "sess-cjk", "中文会话", launch, "c-cjk",
		`{"type":"user","message":{"role":"user","content":"我想查一下六月的飞机票价格和时间"}}`)

	idx, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := idx.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	return idx
}

func TestSearchMatchesBody(t *testing.T) {
	idx := newSeededIndex(t)
	res, err := idx.Search("marketplace socket")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res) == 0 || res[0].JarvisID != "sess-body" {
		t.Fatalf("expected sess-body first, got %+v", res)
	}
	if !strings.Contains(res[0].Snippet, "\x02") {
		t.Errorf("snippet missing highlight markers: %q", res[0].Snippet)
	}
}

func TestSearchNameOutranksBody(t *testing.T) {
	idx := newSeededIndex(t)
	// "deadlock" appears only in sess-name's *name*. It must come first.
	res, err := idx.Search("deadlock")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res) == 0 || res[0].JarvisID != "sess-name" {
		t.Fatalf("expected sess-name first for a name hit, got %+v", res)
	}
	if res[0].Name != "PTY deadlock" {
		t.Errorf("Name = %q", res[0].Name)
	}
}

func TestSearchChineseTrigram(t *testing.T) {
	idx := newSeededIndex(t)
	res, err := idx.Search("飞机票") // 3 chars — satisfies trigram minimum
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res) == 0 || res[0].JarvisID != "sess-cjk" {
		t.Fatalf("expected sess-cjk for Chinese query, got %+v", res)
	}
}

func TestSearchEscapesSpecialChars(t *testing.T) {
	idx := newSeededIndex(t)
	// A query full of FTS5 syntax characters must not error.
	if _, err := idx.Search(`"marketplace" AND (socket*`); err != nil {
		t.Fatalf("special-char query errored: %v", err)
	}
}
