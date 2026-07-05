package searchindex

import (
	"strings"

	"jarvis/internal/model"
	"jarvis/internal/store"
	"jarvis/internal/ui"
)

// Highlight markers wrapped around matched terms by snippet(). The TUI restyles
// them; they are ASCII control chars that never appear in real text.
// Note: markers may legitimately be ABSENT in edge cases (very long matched
// spans exceeding the snippet window, some token-boundary quirks) — consumers
// must not assume they exist in every Result.Snippet.
const (
	MarkOpen  = "\x02"
	MarkClose = "\x03"
)

const untitledPlaceholder = "(untitled chat)"

// Result is one search hit, resolved to a jarvis session.
type Result struct {
	JarvisID string
	Name     string
	Snippet  string
	Age      string
	Status   model.SessionStatus
}

// Search runs a full-text query and returns matching sessions, best first.
func (i *Index) Search(query string) ([]Result, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}
	match := escapeFTSQuery(q)
	if match == "" {
		return nil, nil // query was all control characters
	}

	// 64 tokens: snippet counts tokens, and trigram tokens are 3-char windows,
	// so 64 ≈ one line of context (FTS5 max).
	rows, err := i.db.Query(
		`SELECT f.jarvis_id, COALESCE(m.ai_title,''),
		        snippet(sessions_fts, -1, ?, ?, '…', 64)
		 FROM sessions_fts f
		 JOIN index_meta m ON m.rowid_ref = f.rowid
		 WHERE sessions_fts MATCH ?
		 ORDER BY bm25(sessions_fts, 0.0, 12.0, 8.0, 2.0, 1.0, 3.0)
		 LIMIT 50`,
		MarkOpen, MarkClose, match,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Result
	for rows.Next() {
		var id, aiTitle, snip string
		if err := rows.Scan(&id, &aiTitle, &snip); err != nil {
			return nil, err
		}
		sess, err := store.GetSession(id)
		if err != nil {
			continue // stale row; session gone
		}
		name := sess.Name
		if name == "" || name == untitledPlaceholder {
			if aiTitle != "" {
				name = aiTitle
			}
		}
		results = append(results, Result{
			JarvisID: id,
			Name:     name,
			Snippet:  snip,
			Age:      ui.FormatAge(sess.UpdatedAt),
			Status:   sess.Status,
		})
	}
	return results, rows.Err()
}

// escapeFTSQuery turns arbitrary user input into a safe FTS5 MATCH expression by
// wrapping it as a single quoted phrase (doubling embedded quotes). With the
// trigram tokenizer a quoted phrase matches any substring, which is the
// substring-search behavior we want.
//
// C0 control characters are stripped first: a NUL truncates the bound
// parameter at the C-string boundary (leaving the opening quote unterminated
// — "SQL logic error: unterminated string"), and pasted \x02/\x03 bytes would
// collide with the highlight markers. Tab/newline carry no meaning in a
// search query either, so the whole range goes. Returns "" if nothing
// remains; callers must treat that like an empty query.
func escapeFTSQuery(q string) string {
	q = strings.Map(func(r rune) rune {
		if r < 0x20 {
			return -1
		}
		return r
	}, q)
	if q == "" {
		return ""
	}
	return `"` + strings.ReplaceAll(q, `"`, `""`) + `"`
}
