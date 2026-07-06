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

func TestSearchSnippetIsReadable(t *testing.T) {
	t.Setenv("JARVIS_HOME", t.TempDir())
	launch := t.TempDir()
	// Long sentence with the matched term mid-sentence: the snippet window
	// must be wide enough to give real context, not a ~14-char sliver.
	seedSession(t, "sess-long", "long body", launch, "c-long",
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"When the shard resolver walks the registry it calls resolveHarborByShardForTenant deep inside the routing layer and only afterwards does it consult the fallback table for stale entries."}]}}`)

	idx, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	if _, err := idx.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}

	res, err := idx.Search("HarborByShard")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res) == 0 || res[0].JarvisID != "sess-long" {
		t.Fatalf("expected sess-long, got %+v", res)
	}
	snip := res[0].Snippet
	content := strings.NewReplacer(MarkOpen, "", MarkClose, "", "…", "").Replace(snip)
	if n := len([]rune(content)); n < 40 {
		t.Errorf("snippet too narrow: %d runes of content in %q", n, snip)
	}
	if !strings.Contains(snip, MarkOpen) || !strings.Contains(snip, MarkClose) {
		t.Errorf("snippet missing highlight markers: %q", snip)
	}
}

func TestSearchEscapesSpecialChars(t *testing.T) {
	idx := newSeededIndex(t)
	// Queries full of FTS5 syntax or control characters must not error.
	for _, q := range []string{
		`"marketplace" AND (socket*`,
		"market\x00place", // NUL truncates the bound C string; must not break MATCH
		"abc\x02def\x03",  // pasted highlight-marker bytes must be stripped
		"\x00\x01\x1f",    // control-only query: must behave like an empty query
	} {
		if _, err := idx.Search(q); err != nil {
			t.Errorf("query %q errored: %v", q, err)
		}
	}
}
