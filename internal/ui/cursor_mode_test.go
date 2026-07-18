package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"xcx/internal/sshterm"
)

// TestShiftArrowForwardsInApplicationCursor reproduces the vim arrow-key bug.
// vim enables DECCKM application cursor keys, so its arrow keys arrive as
// ESC O A/B, which bubbletea decodes as KeyShiftUp/Down. The previous local
// scroll-back handler intercepted those keys, so vim never received them and
// arrow navigation stopped working. In application-cursor mode these keys must
// be forwarded to the remote program (as ESC O A/B); local scrolling only
// applies in the normal shell.
func TestShiftArrowForwardsInApplicationCursor(t *testing.T) {
	app := New(Options{})
	app.view = viewMain
	app.right = rightTerminal
	app.focus = focusRight
	app.width, app.height = 100, 20
	screen := sshterm.NewScreen(40)
	term := sshterm.NewTerminalWithScreen(screen)
	app.terminal = terminalModel{term: term}

	var wrote []byte
	app.terminal.writeInput = func(b []byte) error {
		wrote = append(wrote, b...)
		return nil
	}

	// Put the terminal into application cursor mode (as vim does on startup).
	term.FeedOutput([]byte("\x1b[?1h"))

	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyShiftUp})
	if got := string(wrote); got != "\x1bOA" {
		t.Fatalf("application-mode ShiftUp forwarded %q, want \\x1bOA", got)
	}

	wrote = wrote[:0]
	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyShiftDown})
	if got := string(wrote); got != "\x1bOB" {
		t.Fatalf("application-mode ShiftDown forwarded %q, want \\x1bOB", got)
	}
}

// TestShiftArrowScrollsInNormalShell confirms that without application cursor
// mode (a plain shell), Shift+Up/Down still scroll locally and are NOT
// forwarded — the original behavior for reviewing history.
func TestShiftArrowScrollsInNormalShell(t *testing.T) {
	app := New(Options{})
	app.view = viewMain
	app.right = rightTerminal
	app.focus = focusRight
	app.width, app.height = 100, 20
	screen := sshterm.NewScreen(40)
	for i := 0; i < 30; i++ {
		screen.Print('x', sshterm.Style{})
		screen.CarriageReturn()
		screen.LineFeed()
	}
	term := sshterm.NewTerminalWithScreen(screen)
	app.terminal = terminalModel{term: term}
	_, h := app.RightSize()
	_ = screen.View(h)

	var wrote []byte
	app.terminal.writeInput = func(b []byte) error {
		wrote = append(wrote, b...)
		return nil
	}

	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyShiftUp})
	if len(wrote) != 0 {
		t.Fatalf("normal-shell ShiftUp forwarded %q, want local scroll only", string(wrote))
	}
	if off := screen.ScrollOffset(); off != 1 {
		t.Fatalf("normal-shell ShiftUp scrolled to offset %d, want 1", off)
	}
}
