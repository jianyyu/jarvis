package autorename

import (
	"os"
	"path/filepath"
	"testing"

	"jarvis/internal/model"
	"jarvis/internal/paths"
	"jarvis/internal/store"
)

func TestFindCandidates(t *testing.T) {
	sessions := []*model.Session{
		{ID: "a", Name: "(untitled chat)", Status: model.StatusActive, ClaudeSessionID: "csid-a"},
		{ID: "b", Name: "(untitled chat)", Status: model.StatusSuspended, ClaudeSessionID: "csid-b"},
		{ID: "c", Name: "Real Name", Status: model.StatusActive, ClaudeSessionID: "csid-c"},         // named
		{ID: "d", Name: "(untitled chat)", Status: model.StatusDone, ClaudeSessionID: "csid-d"},     // done
		{ID: "e", Name: "(untitled chat)", Status: model.StatusArchived, ClaudeSessionID: "csid-e"}, // archived
		{ID: "f", Name: "(untitled chat)", Status: model.StatusActive, ClaudeSessionID: ""},         // no claude session yet
	}

	got := FindCandidates(sessions)

	var ids []string
	for _, s := range got {
		ids = append(ids, s.ID)
	}
	want := []string{"a", "b"}
	if len(ids) != len(want) {
		t.Fatalf("candidates = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("candidates = %v, want %v", ids, want)
		}
	}
}

type stubGen struct {
	title string
	err   error
	calls []string
}

func (s *stubGen) Title(sess *model.Session) (string, error) {
	s.calls = append(s.calls, sess.ID)
	return s.title, s.err
}

// writeTranscript creates a fake Claude JSONL under the (fake) HOME for the
// given launch dir + claude session id, with one real user message.
func writeTranscript(t *testing.T, home, launchDir, csid string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", paths.EncodeCWD(launchDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","message":{"role":"user","content":"please fix the login bug in the auth service"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, csid+".jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunRenamesUntitledSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("JARVIS_HOME", filepath.Join(home, ".jarvis"))
	launchDir := t.TempDir() // not a git repo, so ProjectDirs has one candidate

	mustSave := func(s *model.Session) {
		t.Helper()
		if err := store.SaveSession(s); err != nil {
			t.Fatal(err)
		}
	}

	// Candidate with a transcript: should be renamed.
	mustSave(&model.Session{ID: "a", Name: UntitledName, Status: model.StatusActive,
		LaunchDir: launchDir, ClaudeSessionID: "csid-a"})
	writeTranscript(t, home, launchDir, "csid-a")

	// Candidate whose transcript is missing: skipped, stays untitled.
	mustSave(&model.Session{ID: "b", Name: UntitledName, Status: model.StatusActive,
		LaunchDir: launchDir, ClaudeSessionID: "csid-missing"})

	// Already named: untouched.
	mustSave(&model.Session{ID: "c", Name: "Keep Me", Status: model.StatusActive,
		LaunchDir: launchDir, ClaudeSessionID: "csid-c"})

	gen := &stubGen{title: "Fix Login Bug"}
	var notified []string
	Run(gen, func(id, name string) { notified = append(notified, id+"="+name) })

	if len(gen.calls) != 1 || gen.calls[0] != "a" {
		t.Fatalf("generator calls = %v, want [a]", gen.calls)
	}
	a, _ := store.GetSession("a")
	if a.Name != "Fix Login Bug" {
		t.Errorf("session a name = %q, want %q", a.Name, "Fix Login Bug")
	}
	b, _ := store.GetSession("b")
	if b.Name != UntitledName {
		t.Errorf("session b name = %q, want untouched", b.Name)
	}
	c, _ := store.GetSession("c")
	if c.Name != "Keep Me" {
		t.Errorf("session c name = %q, want untouched", c.Name)
	}
	if len(notified) != 1 || notified[0] != "a=Fix Login Bug" {
		t.Errorf("notified = %v", notified)
	}
}

func TestRunSkipsOnGeneratorError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("JARVIS_HOME", filepath.Join(home, ".jarvis"))
	launchDir := t.TempDir()

	sess := &model.Session{ID: "a", Name: UntitledName, Status: model.StatusActive,
		LaunchDir: launchDir, ClaudeSessionID: "csid-a"}
	if err := store.SaveSession(sess); err != nil {
		t.Fatal(err)
	}
	writeTranscript(t, home, launchDir, "csid-a")

	gen := &stubGen{err: os.ErrDeadlineExceeded}
	notifyCalled := false
	Run(gen, func(id, name string) { notifyCalled = true })

	got, _ := store.GetSession("a")
	if got.Name != UntitledName {
		t.Errorf("name = %q, want untouched on generator error", got.Name)
	}
	if notifyCalled {
		t.Error("notify must not fire on failure")
	}
}

// renamingGen simulates the user manually renaming the session while the
// slow title generation is in flight.
type renamingGen struct {
	title string
}

func (g *renamingGen) Title(sess *model.Session) (string, error) {
	if _, err := store.RenameSession(sess.ID, "User Chose This"); err != nil {
		return "", err
	}
	return g.title, nil
}

func TestRunDoesNotClobberManualRenameDuringGeneration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("JARVIS_HOME", filepath.Join(home, ".jarvis"))
	launchDir := t.TempDir()

	if err := store.SaveSession(&model.Session{ID: "a", Name: UntitledName, Status: model.StatusActive,
		LaunchDir: launchDir, ClaudeSessionID: "csid-a"}); err != nil {
		t.Fatal(err)
	}
	writeTranscript(t, home, launchDir, "csid-a")

	notifyCalled := false
	Run(&renamingGen{title: "Auto Title"}, func(id, name string) { notifyCalled = true })

	got, _ := store.GetSession("a")
	if got.Name != "User Chose This" {
		t.Errorf("name = %q, want manual rename preserved", got.Name)
	}
	if notifyCalled {
		t.Error("notify must not fire when rename was skipped")
	}
}

// blockingGen blocks inside Title until released, so a test can hold one
// Run in flight while starting another.
type blockingGen struct {
	entered chan struct{}
	release chan struct{}
	titles  []string
}

func (g *blockingGen) Title(sess *model.Session) (string, error) {
	g.entered <- struct{}{}
	<-g.release
	return "Blocked Title", nil
}

func TestRunIsSingleFlight(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("JARVIS_HOME", filepath.Join(home, ".jarvis"))
	launchDir := t.TempDir()

	if err := store.SaveSession(&model.Session{ID: "a", Name: UntitledName, Status: model.StatusActive,
		LaunchDir: launchDir, ClaudeSessionID: "csid-a"}); err != nil {
		t.Fatal(err)
	}
	writeTranscript(t, home, launchDir, "csid-a")

	gen := &blockingGen{entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		Run(gen, nil)
		close(done)
	}()
	<-gen.entered // first Run is now mid-generation

	// Second Run must return immediately without invoking its generator.
	second := &stubGen{title: "Second Title"}
	Run(second, nil)
	if len(second.calls) != 0 {
		t.Errorf("second Run called generator %v; want no calls while another Run is in flight", second.calls)
	}

	close(gen.release)
	<-done

	got, _ := store.GetSession("a")
	if got.Name != "Blocked Title" {
		t.Errorf("name = %q, want first Run's title", got.Name)
	}
}
