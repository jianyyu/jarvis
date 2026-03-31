package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"jarvis/internal/config"
	"jarvis/internal/model"
	"jarvis/internal/session"
	"jarvis/internal/sidecar"
	"jarvis/internal/store"
	"jarvis/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "jarvis",
	Short: "Multi-session Claude Code commander",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDashboard()
	},
}

func runDashboard() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	for {
		dashboard := tui.NewDashboard(cfg)
		p := tea.NewProgram(dashboard, tea.WithAltScreen())
		m, err := p.Run()
		if err != nil {
			return err
		}

		d := m.(tui.Dashboard)
		sessionID := d.AttachSessionID()
		if sessionID == "" {
			return nil // user quit
		}

		// Attach to session
		mgr := session.NewManager(cfg)
		fmt.Printf("Attaching to session... [Ctrl-\\ to detach]\n")
		mgr.Attach(sessionID)

		// After detach, let terminal settle before restarting bubbletea
		fmt.Println("\nDetached. Returning to dashboard...")
		time.Sleep(100 * time.Millisecond)
	}
}

var newCmd = &cobra.Command{
	Use:   "new [name]",
	Short: "Create a new session and launch Claude Code",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := strings.Join(args, " ")
		cfg, err := config.Load()
		if err != nil {
			return err
		}

		cwd := cfg.RepoPath()
		if cwd == "" {
			cwd, _ = os.Getwd()
		}

		mgr := session.NewManager(cfg)

		claudeArgs := []string{"claude"}

		fmt.Printf("Creating session %q...\n", name)
		sess, err := mgr.Spawn(name, cwd, claudeArgs)
		if err != nil {
			return err
		}
		fmt.Printf("Session %s created. Attaching... [Ctrl-\\ to detach]\n", sess.ID)

		return mgr.Attach(sess.ID)
	},
}

var chatCmd = &cobra.Command{
	Use:   "chat",
	Short: "Create a lightweight session (no worktree, main repo)",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}

		cwd := cfg.RepoPath()
		if cwd == "" {
			cwd, _ = os.Getwd()
		}

		mgr := session.NewManager(cfg)
		claudeArgs := []string{"claude"}

		fmt.Println("Creating chat session...")
		sess, err := mgr.Spawn("(untitled chat)", cwd, claudeArgs)
		if err != nil {
			return err
		}
		fmt.Printf("Session %s created. Attaching... [Ctrl-\\ to detach]\n", sess.ID)

		return mgr.Attach(sess.ID)
	},
}

var attachCmd = &cobra.Command{
	Use:   "attach [session-name-or-id]",
	Short: "Attach to a running or suspended session",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}

		sess, err := session.FindSessionByName(args[0])
		if err != nil {
			return err
		}

		mgr := session.NewManager(cfg)
		fmt.Printf("Attaching to %q (%s)... [Ctrl-\\ to detach]\n", sess.Name, sess.ID)
		return mgr.Attach(sess.ID)
	},
}

var lsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List all sessions with status",
	RunE: func(cmd *cobra.Command, args []string) error {
		session.RecoverAllSessions()

		sessions, err := store.ListSessions(nil)
		if err != nil {
			return err
		}

		if len(sessions) == 0 {
			fmt.Println("No sessions.")
			return nil
		}

		cfg, _ := config.Load()
		mgr := session.NewManager(cfg)

		for _, s := range sessions {
			if s.Status == model.StatusArchived {
				continue
			}
			icon := statusIcon(s.Status)
			state := ""
			detail := ""

			if s.Status == model.StatusActive {
				st, det, _ := mgr.GetStatus(s.ID)
				state = st
				detail = det
			} else if s.Status == model.StatusSuspended {
				state = "was: " + s.LastKnownState
				detail = s.LastKnownDetail
			}

			age := formatAge(s.UpdatedAt)
			line := fmt.Sprintf("  %s %-30s %-10s %-25s %s", icon, s.Name, s.Status, state, age)
			if detail != "" {
				line += "  " + truncate(detail, 40)
			}
			fmt.Println(line)
		}
		return nil
	},
}

var statusCmd = &cobra.Command{
	Use:   "status [session-name-or-id]",
	Short: "Show detailed status of a session",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		sess, err := session.FindSessionByName(args[0])
		if err != nil {
			return err
		}

		cfg, _ := config.Load()
		mgr := session.NewManager(cfg)
		state, detail, _ := mgr.GetStatus(sess.ID)

		fmt.Printf("Session: %s\n", sess.Name)
		fmt.Printf("ID:      %s\n", sess.ID)
		fmt.Printf("Status:  %s\n", sess.Status)
		fmt.Printf("State:   %s\n", state)
		if detail != "" {
			fmt.Printf("Detail:  %s\n", detail)
		}
		fmt.Printf("CWD:     %s\n", sess.CWD)
		if sess.ClaudeSessionID != "" {
			fmt.Printf("Claude:  %s\n", sess.ClaudeSessionID)
		}
		if sess.Sidecar != nil {
			fmt.Printf("PID:     %d\n", sess.Sidecar.PID)
			fmt.Printf("Socket:  %s\n", sess.Sidecar.Socket)
		}
		fmt.Printf("Created: %s\n", sess.CreatedAt.Format(time.RFC3339))
		fmt.Printf("Updated: %s\n", sess.UpdatedAt.Format(time.RFC3339))
		return nil
	},
}

var doneCmd = &cobra.Command{
	Use:   "done [session-name-or-id]",
	Short: "Mark a session as done",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		sess, err := session.FindSessionByName(args[0])
		if err != nil {
			return err
		}

		socketPath := sidecar.SocketPath(sess.ID)
		if session.PingSidecar(socketPath) {
			fmt.Println("Session is still running. Use 'jarvis-v2 rm' to stop and delete, or exit Claude first.")
			return nil
		}

		sess.Status = model.StatusDone
		sess.UpdatedAt = time.Now()
		if err := store.SaveSession(sess); err != nil {
			return err
		}
		fmt.Printf("Session %q marked as done.\n", sess.Name)
		return nil
	},
}


var rmCmd = &cobra.Command{
	Use:   "rm [session-name-or-id]",
	Short: "Delete a session permanently",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := strings.Join(args, " ")
		sess, err := session.FindSessionByName(name)
		if err != nil {
			return err
		}

		// Kill sidecar if alive
		socketPath := sidecar.SocketPath(sess.ID)
		if session.PingSidecar(socketPath) {
			fmt.Printf("Session %q is still running. Killing sidecar...\n", sess.Name)
			os.Remove(socketPath)
			if sess.Sidecar != nil && sess.Sidecar.PID > 0 {
				if p, err := os.FindProcess(sess.Sidecar.PID); err == nil {
					p.Signal(syscall.SIGTERM)
				}
			}
		}

		if err := store.DeleteSession(sess.ID); err != nil {
			return err
		}
		fmt.Printf("Deleted session %q (%s).\n", sess.Name, sess.ID)
		return nil
	},
}

func statusIcon(status model.SessionStatus) string {
	switch status {
	case model.StatusActive:
		return "●"
	case model.StatusSuspended:
		return "⏸"
	case model.StatusDone:
		return "✓"
	case model.StatusQueued:
		return "◌"
	case model.StatusArchived:
		return "▪"
	default:
		return "?"
	}
}

func formatAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

var renameCmd = &cobra.Command{
	Use:   "rename [new-name]",
	Short: "Rename a session (uses JARVIS_SESSION_ID or --session-id)",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		newName := strings.Join(args, " ")

		sessionID, _ := cmd.Flags().GetString("session-id")
		if sessionID == "" {
			sessionID = os.Getenv("JARVIS_SESSION_ID")
		}
		if sessionID == "" {
			return fmt.Errorf("no session ID: set JARVIS_SESSION_ID or pass --session-id")
		}

		sess, err := store.GetSession(sessionID)
		if err != nil {
			return fmt.Errorf("session %q not found", sessionID)
		}

		oldName := sess.Name
		sess.Name = newName
		sess.UpdatedAt = time.Now()
		if err := store.SaveSession(sess); err != nil {
			return err
		}
		fmt.Printf("Renamed %q → %q\n", oldName, newName)
		return nil
	},
}

var initCmd = &cobra.Command{
	Use:   "init [title]",
	Short: "Initialize session: rename, create worktree and branch",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		title := strings.Join(args, " ")

		sessionID, _ := cmd.Flags().GetString("session-id")
		if sessionID == "" {
			sessionID = os.Getenv("JARVIS_SESSION_ID")
		}
		if sessionID == "" {
			return fmt.Errorf("no session ID: set JARVIS_SESSION_ID or pass --session-id")
		}

		sess, err := store.GetSession(sessionID)
		if err != nil {
			return fmt.Errorf("session %q not found", sessionID)
		}

		// Rename
		sess.Name = title
		sess.UpdatedAt = time.Now()

		// Generate branch name from title
		slug := slugify(title)

		// Find git repo root from session CWD
		repoRoot := sess.CWD
		gitCmd := exec.Command("git", "-C", repoRoot, "rev-parse", "--show-toplevel")
		if out, err := gitCmd.Output(); err == nil {
			repoRoot = strings.TrimSpace(string(out))
		}

		// Create worktree under the session's repo, not the jarvis process's CWD
		worktreeBase := filepath.Join(repoRoot, ".claude", "worktrees")
		worktreePath := filepath.Join(worktreeBase, slug)

		// Check if worktree already exists
		if _, err := os.Stat(worktreePath); err == nil {
			fmt.Printf("Worktree already exists at %s\n", worktreePath)
			sess.CWD = worktreePath
			return store.SaveSession(sess)
		}

		// Try git stack first, fall back to plain worktree
		branch, useStack := createBranchAndWorktree(repoRoot, slug, worktreePath)
		if branch == "" {
			return fmt.Errorf("failed to create branch and worktree")
		}

		sess.CWD = worktreePath
		if err := store.SaveSession(sess); err != nil {
			return err
		}

		fmt.Printf("Session: %s\n", title)
		fmt.Printf("Worktree: %s\n", worktreePath)
		fmt.Printf("Branch: %s\n", branch)
		if useStack {
			fmt.Println("Mode: git stack (use 'git stack push' to create PR)")
		} else {
			fmt.Println("Mode: plain branch (git stack not available)")
		}
		fmt.Println("\nNote: subsequent code changes should be made in the new worktree directory.")
		return nil
	},
}

// createBranchAndWorktree tries git stack first, falls back to plain git worktree.
// Returns (branch name, whether git stack was used).
func createBranchAndWorktree(repoRoot, slug, worktreePath string) (string, bool) {
	// Check if git stack is available
	checkCmd := exec.Command("git", "-C", repoRoot, "stack", "--version")
	if err := checkCmd.Run(); err == nil {
		// git stack is available — create a stacked branch (no checkout in main repo)
		stackCmd := exec.Command("git", "-C", repoRoot, "stack", "create", slug, "--on", "master", "--no-checkout")
		stackCmd.Stdout = os.Stdout
		stackCmd.Stderr = os.Stderr
		if err := stackCmd.Run(); err == nil {
			stackBranch := "stack/" + slug
			// Create worktree pointing to the existing stacked branch
			wtCmd := exec.Command("git", "-C", repoRoot, "worktree", "add", worktreePath, stackBranch)
			wtCmd.Stdout = os.Stdout
			wtCmd.Stderr = os.Stderr
			if err := wtCmd.Run(); err == nil {
				return stackBranch, true
			}
			fmt.Printf("Warning: worktree creation failed for stacked branch, cleaning up: %v\n", err)
			// Clean up the stacked branch since worktree failed
			cleanCmd := exec.Command("git", "-C", repoRoot, "stack", "remove", slug)
			cleanCmd.Run()
		} else {
			fmt.Printf("Warning: git stack create failed, falling back to plain branch: %v\n", err)
		}
	}

	// Fallback: plain git worktree with a new branch
	wtCmd := exec.Command("git", "-C", repoRoot, "worktree", "add", "-b", slug, worktreePath)
	wtCmd.Stdout = os.Stdout
	wtCmd.Stderr = os.Stderr
	if err := wtCmd.Run(); err != nil {
		fmt.Printf("Error: failed to create worktree: %v\n", err)
		return "", false
	}
	return slug, false
}

// slugify converts a title to a branch-safe slug.
func slugify(title string) string {
	title = strings.ToLower(title)
	// Replace non-alphanumeric with hyphens
	var b strings.Builder
	for _, r := range title {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	slug := b.String()
	// Collapse multiple hyphens
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	slug = strings.Trim(slug, "-")
	// Remove filler words
	fillers := map[string]bool{"the": true, "a": true, "an": true, "is": true, "are": true, "was": true, "were": true, "of": true, "for": true, "to": true, "in": true, "on": true, "at": true, "by": true, "with": true}
	parts := strings.Split(slug, "-")
	var filtered []string
	for _, p := range parts {
		if !fillers[p] && p != "" {
			filtered = append(filtered, p)
		}
	}
	slug = strings.Join(filtered, "-")
	if len(slug) > 50 {
		slug = slug[:50]
		slug = strings.TrimRight(slug, "-")
	}
	return slug
}

func init() {
	renameCmd.Flags().String("session-id", "", "Session ID to rename (defaults to JARVIS_SESSION_ID)")
	initCmd.Flags().String("session-id", "", "Session ID to init (defaults to JARVIS_SESSION_ID)")
}

func main() {
	rootCmd.AddCommand(newCmd, chatCmd, attachCmd, lsCmd, statusCmd, doneCmd, rmCmd, renameCmd, initCmd)
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
