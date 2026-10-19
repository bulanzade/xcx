package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"xcx/internal/sshterm"
)

// mouseForwardHarness wires a terminal whose parser has the given mouse
// enable sequence applied, with writeInput capturing everything sent to the
// remote. It returns the model and a snapshot func for the sent bytes.
func mouseForwardHarness(t *testing.T, enable string) (terminalModel, func() string) {
	t.Helper()
	screen := sshterm.NewScreen(40)
	screen.SetHeight(10)
	term := sshterm.NewTerminalWithScreen(screen)
	term.FeedOutput([]byte(enable))
	var sent strings.Builder
	m := terminalModel{term: term, writeInput: func(b []byte) error {
		sent.Write(b)
		return nil
	}}
	return m, func() string { return sent.String() }
}

func mouseEvent(button tea.MouseButton, action tea.MouseAction, col, row int) terminalMouseMsg {
	return terminalMouseMsg{
		msg:    tea.MouseMsg{Button: button, Action: action},
		col:    col,
		row:    row,
		inside: true,
	}
}

// TestMouseForwardedToRemoteWhenReporting is the regression test for
// "htop's sort options can't be clicked": while the remote program enabled
// xterm mouse reporting (htop sends CSI ?1006;1000 h), clicks over the
// terminal pane must be encoded and forwarded to the PTY instead of driving
// local selection. Coordinates are 1-based cells in the reported encoding.
func TestMouseForwardedToRemoteWhenReporting(t *testing.T) {
	m, sent := mouseForwardHarness(t, "\x1b[?1006;1000h")
	app := &App{}

	// Left press at cell (col 4, row 2) -> SGR press.
	m.handleMouse(app, mouseEvent(tea.MouseButtonLeft, tea.MouseActionPress, 4, 2))
	if got := sent(); got != "\x1b[<0;5;3M" {
		t.Fatalf("press forwarded %q, want \\x1b[<0;5;3M", got)
	}
	// Release -> SGR release ('m' final).
	m.handleMouse(app, mouseEvent(tea.MouseButtonLeft, tea.MouseActionRelease, 4, 2))
	if got := sent(); got != "\x1b[<0;5;3M\x1b[<0;5;3m" {
		t.Fatalf("release forwarded %q", got)
	}
	// Wheel -> button 64/65 press events.
	m.handleMouse(app, mouseEvent(tea.MouseButtonWheelUp, tea.MouseActionPress, 4, 2))
	if got := sent(); !strings.HasSuffix(got, "\x1b[<64;5;3M") {
		t.Fatalf("wheel forwarded %q, want ...\\x1b[<64;5;3M", got)
	}
	// Modifier bits: ctrl+left press -> cb 0+16.
	m.handleMouse(app, terminalMouseMsg{
		msg: tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, Ctrl: true},
		col: 4, row: 2, inside: true,
	})
	if got := sent(); !strings.HasSuffix(got, "\x1b[<16;5;3M") {
		t.Fatalf("ctrl+press forwarded %q", got)
	}
}

// TestMouseShiftBypassesRemoteReporting verifies Shift+wheel keeps scrolling
// local history even while the remote program owns the mouse (xterm
// convention).
func TestMouseShiftBypassesRemoteReporting(t *testing.T) {
	m, sent := mouseForwardHarness(t, "\x1b[?1006;1000h")
	m.handleMouse(&App{}, terminalMouseMsg{
		msg: tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress, Shift: true},
		col: 4, row: 2, inside: true,
	})
	if got := sent(); got != "" {
		t.Fatalf("shift+wheel must stay local, forwarded %q", got)
	}
}

// TestMouseClassicEncoding verifies the 3-byte X10 form used when the remote
// enabled tracking without SGR (?1000 only): ESC [ M then button/col/row each
// biased by 32, release encoded as button 3.
func TestMouseClassicEncoding(t *testing.T) {
	m, sent := mouseForwardHarness(t, "\x1b[?1000h")

	m.handleMouse(&App{}, mouseEvent(tea.MouseButtonLeft, tea.MouseActionPress, 4, 2))
	if got := sent(); got != "\x1b[M %#" {
		t.Fatalf("classic press forwarded %q, want ESC[M space %% %%#", got)
	}
	m.handleMouse(&App{}, mouseEvent(tea.MouseButtonLeft, tea.MouseActionRelease, 4, 2))
	if got := sent(); !strings.HasSuffix(got, "\x1b[M#%#") { // 32+3, 32+5, 32+3
		t.Fatalf("classic release forwarded %q, want trailing ESC[M with button byte # (release)", got)
	}
}

// TestMouseMotionGatedByTrackingMode verifies motion is only forwarded for
// the modes that ask for it: none under ?1000, all motion under ?1003.
func TestMouseMotionGatedByTrackingMode(t *testing.T) {
	m, sent := mouseForwardHarness(t, "\x1b[?1006;1000h")
	m.handleMouse(&App{}, mouseEvent(tea.MouseButtonNone, tea.MouseActionMotion, 4, 2))
	if got := sent(); got != "" {
		t.Fatalf("motion under ?1000 must not be forwarded, got %q", got)
	}

	m2, sent2 := mouseForwardHarness(t, "\x1b[?1006;1003h")
	m2.handleMouse(&App{}, mouseEvent(tea.MouseButtonNone, tea.MouseActionMotion, 4, 2))
	if got := sent2(); got != "\x1b[<35;5;3M" {
		t.Fatalf("motion under ?1003 forwarded %q, want \\x1b[<35;5;3M", got)
	}
}

// TestMouseNotForwardedOutsidePaneOrWithoutReporting keeps the local
// behaviors intact: events outside the pane never reach the remote program,
// and without mouse reporting everything stays local.
func TestMouseNotForwardedOutsidePaneOrWithoutReporting(t *testing.T) {
	m, sent := mouseForwardHarness(t, "\x1b[?1006;1000h")
	msg := mouseEvent(tea.MouseButtonLeft, tea.MouseActionPress, 4, 2)
	msg.inside = false
	m.handleMouse(&App{}, msg)
	if got := sent(); got != "" {
		t.Fatalf("click outside pane forwarded %q", got)
	}

	m2, sent2 := mouseForwardHarness(t, "")
	m2.handleMouse(&App{}, mouseEvent(tea.MouseButtonLeft, tea.MouseActionPress, 4, 2))
	if got := sent2(); got != "" {
		t.Fatalf("click without reporting must stay local, forwarded %q", got)
	}
}

// TestMouseButtonMotionGatedByMode1002 pins ?1002's defining behavior:
// motion is forwarded while a button is held and suppressed without one.
func TestMouseButtonMotionGatedByMode1002(t *testing.T) {
	m, sent := mouseForwardHarness(t, "\x1b[?1006;1002h")

	m.handleMouse(&App{}, mouseEvent(tea.MouseButtonLeft, tea.MouseActionMotion, 4, 2))
	if got := sent(); got != "\x1b[<32;5;3M" { // cb = 0 | 32 motion bit
		t.Fatalf("held-button motion forwarded %q, want \\x1b[<32;5;3M", got)
	}

	m2, sent2 := mouseForwardHarness(t, "\x1b[?1006;1002h")
	m2.handleMouse(&App{}, mouseEvent(tea.MouseButtonNone, tea.MouseActionMotion, 4, 2))
	if got := sent2(); got != "" {
		t.Fatalf("button-less motion under ?1002 must not be forwarded, got %q", got)
	}
}

// TestMouseX10PressOnly pins the ?9 mode: presses forward in the classic
// encoding, releases are suppressed (the X10 protocol has no release).
func TestMouseX10PressOnly(t *testing.T) {
	m, sent := mouseForwardHarness(t, "\x1b[?9h")

	m.handleMouse(&App{}, mouseEvent(tea.MouseButtonLeft, tea.MouseActionPress, 4, 2))
	if got := sent(); got != "\x1b[M %#" {
		t.Fatalf("X10 press forwarded %q", got)
	}
	m.handleMouse(&App{}, mouseEvent(tea.MouseButtonLeft, tea.MouseActionRelease, 4, 2))
	if got := sent(); got != "\x1b[M %#" {
		t.Fatalf("X10 release must be suppressed, got %q", got)
	}
}

// TestMouseX10PressOnlyEvenWithSGR: ?9 requests presses only regardless of
// the encoding — under ?1006 releases must be suppressed too, or
// press-only applications received events they never asked for.
func TestMouseX10PressOnlyEvenWithSGR(t *testing.T) {
	m, sent := mouseForwardHarness(t, "\x1b[?1006;9h")

	m.handleMouse(&App{}, mouseEvent(tea.MouseButtonLeft, tea.MouseActionPress, 4, 2))
	if got := sent(); got != "\x1b[<0;5;3M" {
		t.Fatalf("X10+SGR press forwarded %q", got)
	}
	m.handleMouse(&App{}, mouseEvent(tea.MouseButtonLeft, tea.MouseActionRelease, 4, 2))
	if got := sent(); got != "\x1b[<0;5;3M" {
		t.Fatalf("X10+SGR release must be suppressed, got %q", got)
	}
}

// TestMouseUTF8ReleaseEncoding verifies ?1005 releases carry the classic
// release encoding (button bits forced to 11). Without it press and release
// were byte-identical, so applications saw a second press instead of a
// completed click.
func TestMouseUTF8ReleaseEncoding(t *testing.T) {
	m, sent := mouseForwardHarness(t, "\x1b[?1005;1000h")

	m.handleMouse(&App{}, mouseEvent(tea.MouseButtonLeft, tea.MouseActionPress, 4, 2))
	if got := sent(); got != "\x1b[M %#" {
		t.Fatalf("utf8 press forwarded %q, want ESC[M space %% %%#", got)
	}
	m.handleMouse(&App{}, mouseEvent(tea.MouseButtonLeft, tea.MouseActionRelease, 4, 2))
	if got := sent(); got != "\x1b[M %#\x1b[M#%#" {
		t.Fatalf("utf8 release forwarded %q, want release button byte # (32+3)", got)
	}
}

// TestMouseReleaseOutsidePaneCompletesDrag pins the press/release pairing: a
// press forwarded inside the pane must get its release forwarded even when
// the release lands outside the pane (drag onto a border, release over
// another panel), or the remote program keeps a button stuck down.
func TestMouseReleaseOutsidePaneCompletesDrag(t *testing.T) {
	m, sent := mouseForwardHarness(t, "\x1b[?1006;1000h")
	app := &App{}

	// fwdMousePress lives in the model, so each call must keep the returned
	// model — the same way Update propagates it.
	m, _ = m.handleMouse(app, mouseEvent(tea.MouseButtonLeft, tea.MouseActionPress, 4, 2))
	if got := sent(); got != "\x1b[<0;5;3M" {
		t.Fatalf("press forwarded %q", got)
	}
	outside := mouseEvent(tea.MouseButtonLeft, tea.MouseActionRelease, -3, 2)
	outside.inside = false
	m, _ = m.handleMouse(app, outside)
	// The out-of-pane column clamps to the pane edge (col -3 -> 1).
	if got := sent(); got != "\x1b[<0;5;3M\x1b[<0;1;3m" {
		t.Fatalf("outside release forwarded %q, want SGR release at clamped col 1", got)
	}
	// The pairing already completed: a repeat release sends nothing more.
	m, _ = m.handleMouse(app, outside)
	if got := sent(); got != "\x1b[<0;5;3M\x1b[<0;1;3m" {
		t.Fatalf("unpaired outside release forwarded %q", got)
	}
}
