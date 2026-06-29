package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestScrollKeys_ShiftArrowsNotForwarded asserts that Shift+Up/Down scroll the
// view and are NOT sent to the remote PTY. If they leaked through, programs
// like less/vim would misbehave and the scroll intent would be lost.
func TestScrollKeys_ShiftArrowsNotForwarded(t *testing.T) {
	for _, kt := range []tea.KeyType{tea.KeyShiftUp, tea.KeyShiftDown} {
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
			t.Errorf("%v forwarded %q to PTY, want nothing (scroll is local)",
				kt, string(wrote))
		}
	}
}
