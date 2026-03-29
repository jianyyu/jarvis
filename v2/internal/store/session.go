package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"jarvis/v2/internal/model"

	"gopkg.in/yaml.v3"
)

// JarvisHome returns the base directory for jarvis data.
func JarvisHome() string {
	if h := os.Getenv("JARVIS_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".jarvis")
}

func sessionDir(id string) string {
	return filepath.Join(JarvisHome(), "sessions", id)
}

func sessionPath(id string) string {
	return filepath.Join(sessionDir(id), "session.yaml")
}

func SaveSession(s *model.Session) error {
	data, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}
	return WriteAtomic(sessionPath(s.ID), data)
}

func GetSession(id string) (*model.Session, error) {
	data, err := os.ReadFile(sessionPath(id))
	if err != nil {
		return nil, fmt.Errorf("read session %s: %w", id, err)
	}
	var s model.Session
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("unmarshal session %s: %w", id, err)
	}
	return &s, nil
}

func DeleteSession(id string) error {
	return os.RemoveAll(sessionDir(id))
}

type SessionFilter struct {
	StatusIn []model.SessionStatus
}

func ListSessions(filter *SessionFilter) ([]*model.Session, error) {
	base := filepath.Join(JarvisHome(), "sessions")
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var sessions []*model.Session
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		s, err := GetSession(e.Name())
		if err != nil {
			continue
		}
		if filter != nil && len(filter.StatusIn) > 0 {
			match := false
			for _, st := range filter.StatusIn {
				if s.Status == st {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		sessions = append(sessions, s)
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})
	return sessions, nil
}
