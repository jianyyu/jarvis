package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"jarvis/v2/internal/config"
	"jarvis/v2/internal/model"
	"jarvis/v2/internal/session"
	"jarvis/v2/internal/sidecar"
	"jarvis/v2/internal/store"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "jarvis",
	Short: "Multi-session Claude Code commander",
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

		// Build claude command
		prompt := fmt.Sprintf("You are working on: %q", name)
		claudeArgs := []string{"claude", "--append-system-prompt", prompt}

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
		// First recover dead sidecars
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

		// Kill sidecar if alive
		socketPath := sidecar.SocketPath(sess.ID)
		if session.PingSidecar(socketPath) {
			fmt.Println("Session is still running. Kill it first or detach.")
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

func main() {
	rootCmd.AddCommand(newCmd, chatCmd, attachCmd, lsCmd, statusCmd, doneCmd)
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
