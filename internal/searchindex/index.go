package searchindex

import (
	"database/sql"
	"os"
	"strings"

	"jarvis/internal/paths"
	"jarvis/internal/store"
)

// Sync brings the index up to date with the session store. It (re)indexes any
// session whose transcript is new or whose mtime changed, and removes rows for
// sessions that no longer exist. Returns the number of sessions (re)indexed.
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
		path, mtime, ok := transcriptPathFor(s.LaunchDir, s.WorktreeDir, s.ClaudeSessionID)
		if !ok {
			continue // transcript not found yet
		}

		var (
			storedMtime         int64
			storedName, aiTitle string
			rowidRef            int64
		)
		err := i.db.QueryRow(
			`SELECT indexed_mtime, COALESCE(name,''), COALESCE(ai_title,''), rowid_ref
			 FROM index_meta WHERE jarvis_id=?`, s.ID,
		).Scan(&storedMtime, &storedName, &aiTitle, &rowidRef)
		if err == nil && storedMtime == mtime {
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

func (i *Index) pruneDeleted(live map[string]bool) error {
	rows, err := i.db.Query(`SELECT jarvis_id, rowid_ref FROM index_meta`)
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
		if _, err := i.db.Exec(`DELETE FROM sessions_fts WHERE rowid=?`, r.rowid); err != nil {
			return err
		}
		if _, err := i.db.Exec(`DELETE FROM index_meta WHERE jarvis_id=?`, r.id); err != nil {
			return err
		}
	}
	return nil
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
		p := dir + string(os.PathSeparator) + claudeID + ".jsonl"
		if fi, err := os.Stat(p); err == nil {
			return p, fi.ModTime().UnixNano(), true
		}
	}
	return "", 0, false
}
