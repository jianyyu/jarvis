// Package worktree provides helpers for creating git branches and worktrees
// for Jarvis sessions.
//
// When a session is initialised with "jarvis init <title>", a dedicated git
// worktree is created so the session can make changes without affecting the
// main checkout.  If the "git stack" tool is available (used at Databricks for
// stacked PRs), it is preferred; otherwise a plain "git worktree add -b" is
// used as a fallback.
package worktree

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Slugify converts a human-readable title into a branch-safe slug.
//
// Rules:
//   - lowercased
//   - non-alphanumeric characters become hyphens
//   - consecutive hyphens collapsed
//   - common English filler words removed (the, a, of, for, ...)
//   - truncated to 50 characters
//
// Example: "Fix the broken auth flow" → "fix-broken-auth-flow"
func Slugify(title string) string {
	title = strings.ToLower(title)

	// Replace non-alphanumeric characters with hyphens.
	var b strings.Builder
	for _, r := range title {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	slug := b.String()

	// Collapse runs of hyphens.
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	slug = strings.Trim(slug, "-")

	// Strip filler words that add noise to branch names.
	fillers := map[string]bool{
		"the": true, "a": true, "an": true,
		"is": true, "are": true, "was": true, "were": true,
		"of": true, "for": true, "to": true,
		"in": true, "on": true, "at": true,
		"by": true, "with": true,
	}
	parts := strings.Split(slug, "-")
	var filtered []string
	for _, p := range parts {
		if !fillers[p] && p != "" {
			filtered = append(filtered, p)
		}
	}
	slug = strings.Join(filtered, "-")

	// Keep branch names reasonably short.
	if len(slug) > 50 {
		slug = slug[:50]
		slug = strings.TrimRight(slug, "-")
	}
	return slug
}

// CreateBranchAndWorktree creates a git branch and worktree for a session.
//
// It tries "git stack" first (for Databricks stacked-PR workflows), then falls
// back to a plain "git worktree add -b".
//
// Returns:
//   - branch: the branch name that was created (e.g. "stack/fix-bug" or "fix-bug")
//   - usedStack: true if git-stack was used
//
// Both return values are zero if creation failed.
func CreateBranchAndWorktree(repoRoot, slug, worktreePath string) (branch string, usedStack bool) {
	// Check if git-stack is available.
	checkCmd := exec.Command("git", "-C", repoRoot, "stack", "--version")
	if err := checkCmd.Run(); err == nil {
		// git-stack is available — create a stacked branch without
		// checking it out in the main repo.
		stackCmd := exec.Command("git", "-C", repoRoot, "stack", "create", slug, "--on", "master", "--no-checkout")
		stackCmd.Stdout = os.Stdout
		stackCmd.Stderr = os.Stderr
		if err := stackCmd.Run(); err == nil {
			stackBranch := "stack/" + slug
			// Create a worktree pointing to the stacked branch.
			wtCmd := exec.Command("git", "-C", repoRoot, "worktree", "add", worktreePath, stackBranch)
			wtCmd.Stdout = os.Stdout
			wtCmd.Stderr = os.Stderr
			if err := wtCmd.Run(); err == nil {
				return stackBranch, true
			}
			fmt.Printf("Warning: worktree creation failed for stacked branch, cleaning up: %v\n", err)
			cleanCmd := exec.Command("git", "-C", repoRoot, "stack", "remove", slug)
			cleanCmd.Run()
		} else {
			fmt.Printf("Warning: git stack create failed, falling back to plain branch: %v\n", err)
		}
	}

	// Fallback: plain git worktree with a new branch.
	wtCmd := exec.Command("git", "-C", repoRoot, "worktree", "add", "-b", slug, worktreePath)
	wtCmd.Stdout = os.Stdout
	wtCmd.Stderr = os.Stderr
	if err := wtCmd.Run(); err != nil {
		fmt.Printf("Error: failed to create worktree: %v\n", err)
		return "", false
	}
	return slug, false
}
