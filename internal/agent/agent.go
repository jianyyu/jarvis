// Package agent resolves which LLM coding-agent CLI a jarvis process drives.
//
// The agent is a property of the *process*, not the session: it's resolved
// once from the invoked binary name (os.Args[0]). Launch jarvis as `jarvis`
// and it drives Claude Code; launch it as `ijarvis` (a symlink) and it drives
// Isaac. Everything that process does — new sessions, chats, and resumes of
// any existing session — uses the resolved agent's executable.
//
// Isaac is a Claude Code wrapper: it shares the entire CLI contract
// (--settings hook injection, --resume <uuid>, --append-system-prompt,
// -p --output-format json --fork-session) and the same session transcript, so
// switching agents is only a matter of the executable name.
package agent

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Agent identifies a coding-agent CLI.
type Agent struct {
	Name string // logical name, e.g. "claude" or "isaac"
	Exec string // executable to run (looked up on PATH)
}

var (
	claude = Agent{Name: "claude", Exec: "claude"}
	isaac  = Agent{Name: "isaac", Exec: "isaac"}
)

// FromArgv0 maps an invoked binary name to an agent. The basename is used, so
// absolute paths and symlinks resolve correctly. Unknown names fall back to
// Claude, preserving the original single-agent behavior.
func FromArgv0(arg0 string) Agent {
	base := filepath.Base(arg0)
	// Tolerate a platform-specific suffix such as ".exe".
	base = strings.TrimSuffix(base, filepath.Ext(base))
	switch base {
	case "ijarvis":
		return isaac
	default:
		return claude
	}
}

var (
	currentOnce sync.Once
	current     Agent
)

// Current returns the agent for this process, resolved once from os.Args[0].
func Current() Agent {
	currentOnce.Do(func() {
		current = FromArgv0(os.Args[0])
	})
	return current
}
