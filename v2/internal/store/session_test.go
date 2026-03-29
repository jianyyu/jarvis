package store

import (
	"os"
	"testing"
	"time"

	"jarvis/v2/internal/model"
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
		CWD:       "/tmp/test",
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
	s := &model.Session{ID: "del123", Type: "session", Name: "delete me", Status: model.StatusActive, CreatedAt: now, UpdatedAt: now}
	SaveSession(s)

	if err := DeleteSession("del123"); err != nil {
		t.Fatal(err)
	}
	if _, err := GetSession("del123"); err == nil {
		t.Error("session should be deleted")
	}
}
