// Package paths provides helpers for locating Claude Code project
// directories and session JSONL files on disk.
//
// Claude Code stores per-project data under ~/.claude/projects/<encoded-cwd>/,
// where <encoded-cwd> is the working directory path with every non-alphanumeric
// character replaced by a hyphen.  When the CWD is inside a git worktree, the
// main repo root's project directory is also a valid location.
package paths

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// nonAlphaNum matches any character that is not a-z, A-Z, or 0-9.
var nonAlphaNum = regexp.MustCompile(`[^a-zA-Z0-9]`)

// EncodeCWD converts a filesystem path into the directory name that Claude Code
// uses under ~/.claude/projects/.  For example "/home/user/my-repo" becomes
// "-home-user-my-repo".
func EncodeCWD(cwd string) string {
	return nonAlphaNum.ReplaceAllString(cwd, "-")
}

// ProjectDirs returns the candidate Claude project directories for a given
// working directory.  The first element is always the encoded CWD itself.
// If the CWD lives inside a git repo (or worktree), the repo root's project
// directory is appended as a second candidate.
func ProjectDirs(cwd string) []string {
	home, _ := os.UserHomeDir()
	base := filepath.Join(home, ".claude", "projects")

	dirs := []string{filepath.Join(base, EncodeCWD(cwd))}

	// If CWD differs from the git repo root, add the repo root's dir too.
	cmd := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel")
	if out, err := cmd.Output(); err == nil {
		repoRoot := strings.TrimSpace(string(out))
		if repoRoot != cwd {
			dirs = append(dirs, filepath.Join(base, EncodeCWD(repoRoot)))
		}
	}

	return dirs
}
