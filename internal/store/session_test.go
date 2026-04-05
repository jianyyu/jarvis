package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"jarvis/internal/model"
)

func TestSessionRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("JARVIS_HOME", tmp)
	defer os.Unsetenv("JARVIS_HOME")

	now := time.Now().Truncate(time.Second)
	sess := &model.Session{
		ID:        "abcd1234",
		Type:      "session",
		Name:      "test session",
		Status:    model.StatusActive,
		LaunchDir: "/tmp/test",
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := SaveSession(sess); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := GetSession("abcd1234")
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if loaded.Name != "test session" {
		t.Errorf("name: got %q, want %q", loaded.Name, "test session")
	}
	if loaded.Status != model.StatusActive {
		t.Errorf("status: got %q, want %q", loaded.Status, model.StatusActive)
	}
	if loaded.LaunchDir != "/tmp/test" {
		t.Errorf("launch_dir: got %q, want %q", loaded.LaunchDir, "/tmp/test")
	}
}

func TestSessionLegacyYAMLMigration(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("JARVIS_HOME", tmp)
	defer os.Unsetenv("JARVIS_HOME")

	id := "legacy-1"
	dir := filepath.Join(tmp, "sessions", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`id: legacy-1
type: session
name: x
status: active
cwd: /wt
original_cwd: /repo
created_at: 2020-01-01T00:00:00Z
updated_at: 2020-01-01T00:00:00Z
`)
	path := filepath.Join(dir, "session.yaml")
	if err := os.WriteFile(path, legacy, 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := GetSession(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if loaded.LaunchDir != "/repo" {
		t.Errorf("LaunchDir: got %q, want /repo", loaded.LaunchDir)
	}
	if loaded.WorktreeDir != "/wt" {
		t.Errorf("WorktreeDir: got %q, want /wt", loaded.WorktreeDir)
	}
}

func TestListSessions(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("JARVIS_HOME", tmp)
	defer os.Unsetenv("JARVIS_HOME")

	now := time.Now().Truncate(time.Second)
	for i, name := range []string{"first", "second", "third"} {
		s := &model.Session{
			ID:        model.NewID(),
			Type:      "session",
			Name:      name,
			Status:    model.StatusActive,
			LaunchDir: "/tmp",
			CreatedAt: now.Add(time.Duration(i) * time.Minute),
			UpdatedAt: now.Add(time.Duration(i) * time.Minute),
		}
		if err := SaveSession(s); err != nil {
			t.Fatal(err)
		}
	}

	sessions, err := ListSessions(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 3 {
		t.Fatalf("got %d sessions, want 3", len(sessions))
	}
	// Should be sorted by updated_at descending
	if sessions[0].Name != "third" {
		t.Errorf("first session should be 'third' (most recent), got %q", sessions[0].Name)
	}
}

func TestListSessionsWithFilter(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("JARVIS_HOME", tmp)
	defer os.Unsetenv("JARVIS_HOME")

	now := time.Now().Truncate(time.Second)
	statuses := []model.SessionStatus{model.StatusActive, model.StatusDone, model.StatusActive}
	for i, st := range statuses {
		s := &model.Session{
			ID:        model.NewID(),
			Type:      "session",
			Name:      "s" + string(rune('0'+i)),
			Status:    st,
			LaunchDir: "/tmp",
			CreatedAt: now,
			UpdatedAt: now,
		}
		SaveSession(s)
	}

	active, _ := ListSessions(&SessionFilter{StatusIn: []model.SessionStatus{model.StatusActive}})
	if len(active) != 2 {
		t.Errorf("got %d active, want 2", len(active))
	}
}

func TestDeleteSession(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("JARVIS_HOME", tmp)
	defer os.Unsetenv("JARVIS_HOME")

	now := time.Now()
	s := &model.Session{ID: "del123", Type: "session", Name: "delete me", Status: model.StatusActive, LaunchDir: "/tmp", CreatedAt: now, UpdatedAt: now}
	SaveSession(s)

	if err := DeleteSession("del123"); err != nil {
		t.Fatal(err)
	}
	if _, err := GetSession("del123"); err == nil {
		t.Error("session should be deleted")
	}
}
