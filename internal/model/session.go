package model

import "time"

type SessionStatus string

const (
	StatusQueued    SessionStatus = "queued"
	StatusActive    SessionStatus = "active"
	StatusSuspended SessionStatus = "suspended"
	StatusDone      SessionStatus = "done"
	StatusArchived  SessionStatus = "archived"
)

type SidecarState string

const (
	StateWorking            SidecarState = "working"
	StateWaitingForApproval SidecarState = "waiting_for_approval"
	StateIdle               SidecarState = "idle"
	StateExited             SidecarState = "exited"
)

type SidecarInfo struct {
	PID       int          `yaml:"pid" json:"pid"`
	Socket    string       `yaml:"socket" json:"socket"`
	StartedAt time.Time    `yaml:"started_at" json:"started_at"`
	State     SidecarState `yaml:"state" json:"state"`
}

type Session struct {
	ID              string        `yaml:"id"`
	Type            string        `yaml:"type"`
	Name            string        `yaml:"name"`
	Status          SessionStatus `yaml:"status"`
	ParentID        string        `yaml:"parent_id,omitempty"`
	CWD             string        `yaml:"cwd"`
	OriginalCWD     string        `yaml:"original_cwd,omitempty"`
	Branches        []string      `yaml:"branches,omitempty"`
	ClaudeSessionID string        `yaml:"claude_session_id,omitempty"`

	Sidecar         *SidecarInfo `yaml:"sidecar,omitempty"`
	LastKnownState  string       `yaml:"last_known_state,omitempty"`
	LastKnownDetail string       `yaml:"last_known_detail,omitempty"`
	LastActivityAt  *time.Time   `yaml:"last_activity_at,omitempty"`

	CreatedAt  time.Time  `yaml:"created_at"`
	UpdatedAt  time.Time  `yaml:"updated_at"`
	ArchivedAt *time.Time `yaml:"archived_at,omitempty"`
}

type Folder struct {
	ID         string     `yaml:"id"`
	Type       string     `yaml:"type"`
	Name       string     `yaml:"name"`
	ParentID   string     `yaml:"parent_id,omitempty"`
	Children   []ChildRef `yaml:"children,omitempty"`
	Status     string     `yaml:"status"`
	CreatedAt  time.Time  `yaml:"created_at"`
	ArchivedAt *time.Time `yaml:"archived_at,omitempty"`
}

type ChildRef struct {
	Type string `yaml:"type"`
	ID   string `yaml:"id"`
}
