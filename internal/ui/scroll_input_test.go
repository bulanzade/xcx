package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"xcx/internal/sshterm"
)

// TestPgKeysAlwaysScrollLocally asserts that PgUp/PgDn are consumed locally
// for scrolling and NEVER forwarded to the remote shell. Previously they were
// forwarded in live mode, which bash readline interpreted as history
// navigation — the user saw the command line flip between history entries
// instead of scrolling the terminal. After the fix they must produce no PTY
// bytes (the scroll itself targets m.term; with a writeInput-only model there
// is nothing to scroll, but the key must still be intercepted).
func TestPgKeysAlwaysScrollLocally(t *testing.T) {
	for _, kt := range []tea.KeyType{tea.KeyPgUp, tea.KeyPgDown} {
		app := New(Options{})
		app.view = viewMain
		app.right = rightTerminal
		app.focus = focusRight
		var wrote []byte
		app.terminal = terminalModel{
			writeInput: func(input []byte) error {
				wrote = append(wrote, input...)
				return nil
			},
		}
		_, _ = app.Update(tea.KeyMsg{Type: kt})
		if len(wrote) != 0 {
			t.Errorf("%v forwarded %q to PTY, want nothing (always local scroll)", kt, string(wrote))
		}
	}
}

// newTerminalWithScrollback attaches a real terminal (with a screen preloaded
// with `rows` lines of scrollback) to app.terminal and returns the screen so
// tests can assert on the scroll offset. It mirrors how terminalStartedMsg
// wires a terminal but without a live SSH session.
func newTerminalWithScrollback(app *App, rows int) *sshterm.Screen {
	screen := sshterm.NewScreen(40)
	for r := 0; r < rows; r++ {
		screen.Print('L', sshterm.Style{})
		screen.CarriageReturn()
		screen.LineFeed()
	}
	term := sshterm.NewTerminalWithScreen(screen)
	app.terminal = terminalModel{term: term}
	return screen
}

// TestVimJumpKeys_G_G jumps to the top / bottom of scrollback with g / G, but
// ONLY while reviewing history. We replace Ctrl+PgUp/PgDn with these because
// bubbletea's Windows console-input layer can't distinguish Ctrl+PgUp/PgDn from
// plain PgUp/PgDn, so the Ctrl variants never arrived on Windows.
//
// g/G are plain letters that the shell must still receive in live mode (the
// user types them as part of commands like `git`), so they are intercepted
// solely when scrollOff > 0.
func TestVimJumpKeys_G_G(t *testing.T) {
	app := New(Options{})
	app.view = viewMain
	app.right = rightTerminal
	app.focus = focusRight
	app.width, app.height = 100, 40
	screen := newTerminalWithScrollback(app, 60)
	// Seed the terminal height so the scroll clamp is tight (View no longer
	// seeds it; a real terminal sets it via resize).
	_, h := app.RightSize()
	screen.SetHeight(h)
	maxOff := screen.Rows() - h // top-most reachable offset given this view height

	// Enter scrollback, then 'G' must NOT jump to bottom — we're already
	// scrolling, but let's verify 'g' jumps to the very top from mid-history.
	screen.Scroll(10) // up into history
	if off := screen.ScrollOffset(); off != 10 {
		t.Fatalf("setup offset = %d, want 10", off)
	}

	// 'g' while in scrollback → jump to top (max offset).
	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if off := screen.ScrollOffset(); off != maxOff {
		t.Errorf("after 'g' offset = %d, want %d (top)", off, maxOff)
	}

	// 'G' while in scrollback → jump back to live (bottom).
	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	if off := screen.ScrollOffset(); off != 0 {
		t.Errorf("after 'G' offset = %d, want 0 (live)", off)
	}
}

// TestVimJumpKeys_PassthroughInLiveMode ensures 'g' and 'G' reach the remote
// shell when NOT reviewing history, so the user can still type them.
func TestVimJumpKeys_PassthroughInLiveMode(t *testing.T) {
	for _, r := range []rune{'g', 'G'} {
		app := New(Options{})
		app.view = viewMain
		app.right = rightTerminal
		app.focus = focusRight
		var wrote []byte
		app.terminal = terminalModel{
			writeInput: func(input []byte) error {
				wrote = append(wrote, input...)
				return nil
			},
		}
		_, _ = app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		if string(wrote) != string(r) {
			t.Errorf("live-mode %q forwarded %q, want %q", r, string(wrote), string(r))
		}
	}
}
