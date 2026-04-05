package watch

import (
	"os"
	"testing"

	"jarvis/internal/model"
	"jarvis/internal/store"
)

func TestEnsureFolderCreatesNew(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("JARVIS_HOME", tmp)
	defer os.Unsetenv("JARVIS_HOME")

	d := &Daemon{}
	folderID, err := d.ensureFolder("Slack")
	if err != nil {
		t.Fatalf("ensureFolder: %v", err)
	}
	if folderID == "" {
		t.Fatal("folder ID should not be empty")
	}

	f, err := store.GetFolder(folderID)
	if err != nil {
		t.Fatalf("get folder: %v", err)
	}
	if f.Name != "Slack" {
		t.Errorf("folder name: got %q", f.Name)
	}
}

func TestEnsureFolderReusesExisting(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("JARVIS_HOME", tmp)
	defer os.Unsetenv("JARVIS_HOME")

	existing := &model.Folder{
		ID:     model.NewID(),
		Type:   "folder",
		Name:   "Slack",
		Status: "active",
	}
	store.SaveFolder(existing)

	d := &Daemon{}
	folderID, err := d.ensureFolder("Slack")
	if err != nil {
		t.Fatalf("ensureFolder: %v", err)
	}
	if folderID != existing.ID {
		t.Errorf("should reuse existing folder, got %q want %q", folderID, existing.ID)
	}
}

func TestPlaceSessionInFolder(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("JARVIS_HOME", tmp)
	defer os.Unsetenv("JARVIS_HOME")

	folder := &model.Folder{
		ID:     model.NewID(),
		Type:   "folder",
		Name:   "Slack",
		Status: "active",
	}
	store.SaveFolder(folder)

	sess := &model.Session{
		ID:        model.NewID(),
		Type:      "session",
		Name:      "test",
		Status:    model.StatusActive,
		LaunchDir: "/tmp",
	}
	store.SaveSession(sess)

	d := &Daemon{}
	d.placeSessionInFolder(sess.ID, folder.ID)

	updated, _ := store.GetFolder(folder.ID)
	if len(updated.Children) != 1 {
		t.Fatalf("children: got %d, want 1", len(updated.Children))
	}
	if updated.Children[0].ID != sess.ID {
		t.Errorf("child ID: got %q", updated.Children[0].ID)
	}

	updatedSess, _ := store.GetSession(sess.ID)
	if updatedSess.ParentID != folder.ID {
		t.Errorf("parent ID: got %q", updatedSess.ParentID)
	}
}
