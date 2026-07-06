package tui

import (
	"strings"
	"unicode/utf8"

	"jarvis/internal/model"
	"jarvis/internal/searchindex"
)

// fullTextEligible reports whether the current query is served by the FTS
// index: the index must be open and the query long enough for the trigram
// tokenizer to match anything.
func (d Dashboard) fullTextEligible() bool {
	return d.idx != nil && utf8.RuneCountInString(d.searchQuery) >= 3
}

// searchResultsActive reports whether the visible list is 2-line FTS results
// (row + snippet). Every piece of layout math that depends on row height —
// the render window and adjustScroll — must use this one
// predicate so they can never disagree.
func (d Dashboard) searchResultsActive() bool {
	return d.mode == ModeSearch && d.fullTextEligible()
}

// fullTextItems runs the FTS query and maps hits to ListItems. The snippet is
// carried in Detail; markers are restyled at render time. On a Search error
// it returns an empty (non-nil) slice — an honest "no results" rather than
// silently switching to name-substring semantics.
func (d Dashboard) fullTextItems() []ListItem {
	if d.idx == nil {
		return []ListItem{}
	}
	results, err := d.idx.Search(d.searchQuery)
	if err != nil {
		return []ListItem{}
	}
	items := make([]ListItem, 0, len(results))
	for _, r := range results {
		items = append(items, ListItem{
			Type:   ItemSession,
			ID:     r.JarvisID,
			Name:   r.Name,
			Status: r.Status,
			State:  model.SidecarState(r.State),
			Detail: r.Snippet,
			Age:    r.Age,
		})
	}
	return items
}

// styleSnippet converts FTS highlight markers into a lipgloss style and
// collapses newlines so the snippet renders on one line. Snippets without
// markers pass through unchanged.
func styleSnippet(s string) string {
	// Collapse whitespace control chars to spaces and strip the rest — e.g.
	// ESC from ANSI sequences pasted into transcripts, which would otherwise
	// corrupt the rendered line. Only the FTS highlight markers survive; the
	// loop below consumes them.
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t' || r == '\r':
			return ' '
		case r == 0x02 || r == 0x03: // searchindex.MarkOpen / MarkClose
			return r
		case r < 0x20 || r == 0x7f:
			return -1
		}
		return r
	}, s)
	var b strings.Builder
	for {
		start := strings.Index(s, searchindex.MarkOpen)
		if start < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:start])
		rest := s[start+len(searchindex.MarkOpen):]
		end := strings.Index(rest, searchindex.MarkClose)
		if end < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(snippetHighlightStyle.Render(rest[:end]))
		s = rest[end+len(searchindex.MarkClose):]
	}
	return b.String()
}
