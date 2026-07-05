package tui

import (
	"strings"

	"jarvis/internal/searchindex"
)

// fullTextItems runs the FTS query and maps hits to ListItems. The snippet is
// carried in Detail; markers are restyled at render time.
func (d Dashboard) fullTextItems() []ListItem {
	if d.idx == nil {
		return nil
	}
	results, err := d.idx.Search(d.searchQuery)
	if err != nil {
		return nil
	}
	items := make([]ListItem, 0, len(results))
	for _, r := range results {
		items = append(items, ListItem{
			Type:   ItemSession,
			ID:     r.JarvisID,
			Name:   r.Name,
			Status: r.Status,
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
	s = strings.ReplaceAll(s, "\n", " ")
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
