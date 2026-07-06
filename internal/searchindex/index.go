package searchindex

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"

	"jarvis/internal/paths"
	"jarvis/internal/store"
)

// Sync brings the index up to date with the session store. It (re)indexes any
// session whose transcript is new or whose mtime changed, refreshes the name
// of sessions renamed in the store (also counted in the returned total), and
// removes rows for sessions that no longer exist. Returns the number of
// sessions (re)indexed.
func (i *Index) Sync() (int, error) {
	sessions, err := store.ListSessions(nil)
	if err != nil {
		return 0, err
	}

	live := make(map[string]bool, len(sessions))
	reindexed := 0

	for _, s := range sessions {
		live[s.ID] = true
		if s.ClaudeSessionID == "" {
			continue // no transcript to index
		}

		var (
			storedMtime                     int64
			storedName, aiTitle, storedPath string
			rowidRef                        int64
		)
		metaErr := i.db.QueryRow(
			`SELECT indexed_mtime, COALESCE(name,''), COALESCE(ai_title,''), COALESCE(transcript_path,''), rowid_ref
			 FROM index_meta WHERE jarvis_id=?`, s.ID,
		).Scan(&storedMtime, &storedName, &aiTitle, &storedPath, &rowidRef)
		haveMeta := metaErr == nil

		// Locate the transcript: stat the stored path first — on the common
		// nothing-changed path this avoids paths.ProjectDirs, which spawns a
		// `git rev-parse` subprocess per session. Fall back to the full
		// ProjectDirs lookup only when there is no stored path or it vanished.
		var path string
		var mtime int64
		if haveMeta && storedPath != "" {
			if fi, statErr := os.Stat(storedPath); statErr == nil {
				path, mtime = storedPath, fi.ModTime().UnixNano()
			}
		}
		if path == "" {
			p, m, ok := transcriptPathFor(s.LaunchDir, s.WorktreeDir, s.ClaudeSessionID)
			if !ok {
				continue // transcript not found yet
			}
			path, mtime = p, m
		}

		if haveMeta && storedMtime == mtime {
			if path != storedPath {
				// Transcript moved but is otherwise unchanged: repair the
				// stored path so future Syncs stat it directly again.
				if _, err := i.db.Exec(
					`UPDATE index_meta SET transcript_path=? WHERE jarvis_id=?`, path, s.ID,
				); err != nil {
					return reindexed, err
				}
			}
			if storedName == s.Name {
				continue // up to date
			}
			// Transcript unchanged but the session was renamed: update the
			// name and metadata columns in place without reparsing the JSONL.
			if err := i.rename(s.ID, rowidRef, s.Name, aiTitle, s.LaunchDir); err != nil {
				return reindexed, err
			}
			reindexed++
			continue
		}

		f, err := os.Open(path)
		if err != nil {
			continue
		}
		ps, perr := ParseTranscript(f)
		f.Close()
		if perr != nil {
			continue
		}

		metadata := strings.Join([]string{s.Name, ps.AITitle, s.LaunchDir}, " ")
		if err := i.upsert(s.ID, s.Name, ps, metadata, path, mtime); err != nil {
			return reindexed, err
		}
		reindexed++
	}

	// Remove rows whose session is gone.
	if err := i.pruneDeleted(live); err != nil {
		return reindexed, err
	}
	return reindexed, nil
}

// upsert replaces a session's FTS row and meta row inside a transaction,
// reusing the FTS rowid so index_meta stays valid.
func (i *Index) upsert(jarvisID, name string, ps ParsedSession, metadata, path string, mtime int64) error {
	tx, err := i.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var oldRowid int64
	err = tx.QueryRow(`SELECT rowid_ref FROM index_meta WHERE jarvis_id=?`, jarvisID).Scan(&oldRowid)
	if err == nil {
		if _, err := tx.Exec(`DELETE FROM sessions_fts WHERE rowid=?`, oldRowid); err != nil {
			return err
		}
	} else if err != sql.ErrNoRows {
		return err
	}

	res, err := tx.Exec(
		`INSERT INTO sessions_fts(jarvis_id, name, initial_prompt, user_text, assistant_text, metadata)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		jarvisID, name, ps.InitialPrompt, ps.UserText, ps.AssistantText, metadata,
	)
	if err != nil {
		return err
	}
	newRowid, err := res.LastInsertId()
	if err != nil {
		return err
	}

	if _, err := tx.Exec(
		`INSERT INTO index_meta(jarvis_id, rowid_ref, transcript_path, indexed_mtime, ai_title, name)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(jarvis_id) DO UPDATE SET
		   rowid_ref=excluded.rowid_ref,
		   transcript_path=excluded.transcript_path,
		   indexed_mtime=excluded.indexed_mtime,
		   ai_title=excluded.ai_title,
		   name=excluded.name`,
		jarvisID, newRowid, path, mtime, ps.AITitle, name,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// rename updates a session's name (and derived metadata) in place when the
// transcript itself is unchanged, avoiding a full reparse of the JSONL.
func (i *Index) rename(jarvisID string, rowid int64, name, aiTitle, launchDir string) error {
	tx, err := i.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	metadata := strings.Join([]string{name, aiTitle, launchDir}, " ")
	if _, err := tx.Exec(
		`UPDATE sessions_fts SET name=?, metadata=? WHERE rowid=?`, name, metadata, rowid,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE index_meta SET name=? WHERE jarvis_id=?`, name, jarvisID,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// pruneDeleted removes index rows for sessions no longer in the store. All
// deletes run in one transaction: a partial prune would leave an index_meta
// row pointing at a freed FTS rowid, which SQLite may hand to a later insert
// for a live session — the next prune would then delete that live session's
// FTS content.
func (i *Index) pruneDeleted(live map[string]bool) error {
	tx, err := i.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT jarvis_id, rowid_ref FROM index_meta`)
	if err != nil {
		return err
	}
	type row struct {
		id    string
		rowid int64
	}
	var stale []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.rowid); err != nil {
			rows.Close()
			return err
		}
		if !live[r.id] {
			stale = append(stale, r)
		}
	}
	rows.Close()

	for _, r := range stale {
		if _, err := tx.Exec(`DELETE FROM sessions_fts WHERE rowid=?`, r.rowid); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM index_meta WHERE jarvis_id=?`, r.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// transcriptPathFor locates a session's transcript JSONL and returns its path
// and mtime (unix nanos). It tries the launch dir and, if set, the worktree
// dir, mirroring resume logic.
func transcriptPathFor(launchDir, worktreeDir, claudeID string) (string, int64, bool) {
	var candidates []string
	for _, dir := range paths.ProjectDirs(launchDir) {
		candidates = append(candidates, dir)
	}
	if worktreeDir != "" && worktreeDir != launchDir {
		for _, dir := range paths.ProjectDirs(worktreeDir) {
			candidates = append(candidates, dir)
		}
	}
	for _, dir := range candidates {
		p := filepath.Join(dir, claudeID+".jsonl")
		if fi, err := os.Stat(p); err == nil {
			return p, fi.ModTime().UnixNano(), true
		}
	}
	return "", 0, false
}
