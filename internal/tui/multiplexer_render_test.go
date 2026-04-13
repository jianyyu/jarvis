package tui

import (
	"io"
	"testing"
	"time"

	"jarvis/internal/config"

	tea "github.com/charmbracelet/bubbletea"
)

// TestMultiplexer_RenderAfterAttach verifies that the Bubble Tea program
// stays responsive after processing a sessionAttachedMsg. This reproduces
// the freeze where View() stops being called after focus switches to TermPane.
func TestMultiplexer_RenderAfterAttach(t *testing.T) {
	cfg := &config.Config{}
	m := NewMultiplexer(cfg)

	// Simulate window size
	model, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = model.(Multiplexer)

	// Simulate sessionAttachedMsg
	model, cmd := m.Update(sessionAttachedMsg{sessionID: "test123"})
	m = model.(Multiplexer)

	if m.focus.Current() != FocusTermPane {
		t.Error("expected focus on TermPane after attach")
	}

	// View() should NOT hang
	done := make(chan string, 1)
	go func() {
		done <- m.View()
	}()

	select {
	case view := <-done:
		if view == "" {
			t.Error("expected non-empty view")
		}
		t.Logf("View returned %d bytes", len(view))
	case <-time.After(2 * time.Second):
		t.Fatal("View() hung after sessionAttachedMsg — this is the freeze bug")
	}

	// Call View() multiple times (simulating redraw ticks)
	for i := 0; i < 5; i++ {
		model, _ = m.Update(termPaneRedrawMsg{})
		m = model.(Multiplexer)

		ch := make(chan string, 1)
		go func() {
			ch <- m.View()
		}()

		select {
		case <-ch:
			// OK
		case <-time.After(2 * time.Second):
			t.Fatalf("View() hung on redraw tick %d", i)
		}
	}

	_ = cmd
}

// TestMultiplexer_FullProgramRender tests the actual tea.Program rendering
// pipeline end-to-end. This catches issues in Bubble Tea's diff renderer
// that pure model tests miss.
func TestMultiplexer_FullProgramRender(t *testing.T) {
	cfg := &config.Config{}
	m := NewMultiplexer(cfg)

	// Use pipes for input/output to run headlessly.
	// Use a slow writer to simulate SSH backpressure.
	inR, inW := io.Pipe()
	defer inW.Close()
	outR, outW := io.Pipe()
	defer outW.Close()

	// Drain output slowly to simulate SSH — read 1KB at a time with 10ms delay
	go func() {
		buf := make([]byte, 1024)
		for {
			_, err := outR.Read(buf)
			if err != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	p := tea.NewProgram(m,
		tea.WithInput(inR),
		tea.WithOutput(outW),
	)
	m.SetProgram(p)

	// Run program in background
	done := make(chan error, 1)
	go func() {
		_, err := p.Run()
		done <- err
	}()

	// Give it time to start and render initial frame
	time.Sleep(300 * time.Millisecond)

	// Use a large window size like the real terminal
	p.Send(tea.WindowSizeMsg{Width: 200, Height: 50})
	time.Sleep(100 * time.Millisecond)

	p.Send(sessionAttachedMsg{sessionID: "test-render"})
	time.Sleep(100 * time.Millisecond)

	// Send a few redraw ticks
	for i := 0; i < 3; i++ {
		p.Send(termPaneRedrawMsg{})
		time.Sleep(100 * time.Millisecond)
	}

	// If we get here, the program is still responsive. Send quit.
	p.Send(tea.KeyMsg{Type: tea.KeyEscape})
	time.Sleep(100 * time.Millisecond)
	p.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})

	// Wait for program to exit
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("program error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("program hung — did not exit within 5 seconds after quit")
	}

	t.Log("Program exited cleanly — no freeze")
}
