package tui

import (
	"path/filepath"
	"testing"

	"jarvis/internal/searchindex"
)

func TestStyleSnippet(t *testing.T) {
	hl := snippetHighlightStyle.Render
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "no markers passes through unchanged",
			in:   "plain snippet text",
			want: "plain snippet text",
		},
		{
			name: "newlines and tabs collapse to spaces",
			in:   "line one\nline two\tend",
			want: "line one line two end",
		},
		{
			name: "two marked spans both styled",
			in:   "pre \x02foo\x03 mid \x02bar\x03 post",
			want: "pre " + hl("foo") + " mid " + hl("bar") + " post",
		},
		{
			name: "MarkOpen without MarkClose: text preserved, no corruption",
			in:   "pre \x02rest of snippet",
			want: "pre rest of snippet",
		},
		{
			name: "ANSI ESC stripped",
			in:   "\x1b[31mred text",
			want: "[31mred text",
		},
		{
			// Current behavior: a stray MarkClose with no preceding MarkOpen
			// is not consumed by the marker loop and passes through verbatim.
			name: "stray MarkClose alone passes through",
			in:   "a\x03b",
			want: "a\x03b",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := styleSnippet(tt.in); got != tt.want {
				t.Errorf("styleSnippet(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestMaxVisibleItems(t *testing.T) {
	idx, err := searchindex.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer idx.Close()

	tests := []struct {
		name string
		d    Dashboard
		want int
	}{
		{
			name: "dashboard mode: full viewport (height-4)",
			d:    Dashboard{height: 30},
			want: 26,
		},
		{
			name: "search results active: (viewport-2)/2 for 2-line rows + help hint",
			d:    Dashboard{height: 30, mode: ModeSearch, searchQuery: "abc", idx: idx},
			want: 12,
		},
		{
			name: "short query: not FTS-active, full viewport",
			d:    Dashboard{height: 30, mode: ModeSearch, searchQuery: "ab", idx: idx},
			want: 26,
		},
		{
			name: "nil index: not FTS-active, full viewport",
			d:    Dashboard{height: 30, mode: ModeSearch, searchQuery: "abc"},
			want: 26,
		},
		{
			name: "dashboard mode with query (post-enter filtered view): full viewport",
			d:    Dashboard{height: 30, mode: ModeDashboard, searchQuery: "abc", idx: idx},
			want: 26,
		},
		{
			name: "tiny terminal clamps to at least 1 item",
			d:    Dashboard{height: 0, mode: ModeSearch, searchQuery: "abc", idx: idx},
			want: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.d.maxVisibleItems(); got != tt.want {
				t.Errorf("maxVisibleItems() = %d, want %d", got, tt.want)
			}
		})
	}
}
