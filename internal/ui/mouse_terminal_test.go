package ui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"xcx/internal/sshterm"
)

type fakeClipboard struct {
	copied string
	paste  string
	err    error
}

func (f *fakeClipboard) Copy(text string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.copied = text
	return "fake", nil
}

func (f *fakeClipboard) Paste() (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.paste, nil
}

func newMouseTerminalApp(t *testing.T, clip appClipboard) (*App, *sshterm.Screen) {
	t.Helper()
	app := New(Options{Clipboard: clip})
	app.view = viewMain
	app.right = rightTerminal
	app.focus = focusRight
	app.width, app.height = 100, 20
	screen := sshterm.NewScreen(40)
	p := sshterm.NewParser(screen)
	p.Write([]byte("hello world\r\nsecond line"))
	app.terminal = terminalModel{term: sshterm.NewTerminalWithScreen(screen)}
	return app, screen
}

func terminalMouseCoords(app *App, col, row int) (int, int) {
	leftW, _, _ := app.layout()
	return leftW + 2 + col, 1 + row
}

func sendTerminalMouse(app *App, button tea.MouseButton, action tea.MouseAction, col, row int) {
	x, y := terminalMouseCoords(app, col, row)
	_, _ = app.Update(tea.MouseMsg{Button: button, Action: action, X: x, Y: y})
}

func TestTerminalMouseWheelScrollsLocally(t *testing.T) {
	app, screen := newMouseTerminalApp(t, &fakeClipboard{})
	for i := 0; i < 40; i++ {
		screen.Print('x', sshterm.Style{})
		screen.CarriageReturn()
		screen.LineFeed()
	}
	_, h := app.RightSize()
	_ = screen.View(h)

	sendTerminalMouse(app, tea.MouseButtonWheelUp, tea.MouseActionPress, 0, 0)

	if off := screen.ScrollOffset(); off != 3 {
		t.Fatalf("scroll offset = %d, want 3", off)
	}
}

func TestTerminalMouseWheelKeepsSelection(t *testing.T) {
	clip := &fakeClipboard{}
	app, screen := newMouseTerminalApp(t, clip)
	for i := 0; i < 40; i++ {
		screen.Print('x', sshterm.Style{})
		screen.CarriageReturn()
		screen.LineFeed()
	}
	_, h := app.RightSize()
	_ = screen.View(h)

	sendTerminalMouse(app, tea.MouseButtonLeft, tea.MouseActionPress, 0, 0)
	sendTerminalMouse(app, tea.MouseButtonLeft, tea.MouseActionRelease, 4, 0)
	sendTerminalMouse(app, tea.MouseButtonWheelUp, tea.MouseActionPress, 0, 0)

	if !app.terminal.hasSelection() {
		t.Fatal("wheel scrolling should keep selection")
	}

	sendTerminalMouse(app, tea.MouseButtonRight, tea.MouseActionPress, 4, 0)
	if clip.copied == "" {
		t.Fatal("selection was not copied after wheel scroll")
	}
}

func TestTerminalLeftSelectRightCopy(t *testing.T) {
	clip := &fakeClipboard{}
	app, _ := newMouseTerminalApp(t, clip)

	sendTerminalMouse(app, tea.MouseButtonLeft, tea.MouseActionPress, 0, 0)
	sendTerminalMouse(app, tea.MouseButtonLeft, tea.MouseActionMotion, 4, 0)
	sendTerminalMouse(app, tea.MouseButtonLeft, tea.MouseActionRelease, 4, 0)

	if clip.copied != "" {
		t.Fatalf("left release copied %q, want no copy until right click", clip.copied)
	}

	sendTerminalMouse(app, tea.MouseButtonRight, tea.MouseActionPress, 4, 0)

	if clip.copied != "hello" {
		t.Fatalf("copied = %q, want hello", clip.copied)
	}
	if app.terminal.hasSelection() {
		t.Fatal("selection should clear after right-copy")
	}
	if !strings.Contains(app.status, "copied 5 chars") {
		t.Fatalf("status = %q, want copied count", app.status)
	}
}

func TestTerminalRightReleaseDoesNotCopyAgain(t *testing.T) {
	clip := &fakeClipboard{}
	app, _ := newMouseTerminalApp(t, clip)

	sendTerminalMouse(app, tea.MouseButtonLeft, tea.MouseActionPress, 0, 0)
	sendTerminalMouse(app, tea.MouseButtonLeft, tea.MouseActionRelease, 4, 0)
	sendTerminalMouse(app, tea.MouseButtonRight, tea.MouseActionPress, 4, 0)
	clip.copied = ""
	sendTerminalMouse(app, tea.MouseButtonRight, tea.MouseActionRelease, 4, 0)

	if clip.copied != "" {
		t.Fatalf("right release copied %q, want no duplicate copy", clip.copied)
	}
}

func TestTerminalSelectionClearsWhenOutputChanges(t *testing.T) {
	clip := &fakeClipboard{paste: "should-not-paste"}
	app, screen := newMouseTerminalApp(t, clip)
	var wrote []byte
	app.terminal.writeInput = func(input []byte) error {
		wrote = append(wrote, input...)
		return nil
	}

	sendTerminalMouse(app, tea.MouseButtonLeft, tea.MouseActionPress, 0, 0)
	sendTerminalMouse(app, tea.MouseButtonLeft, tea.MouseActionRelease, 4, 0)
	if !app.terminal.hasSelection() {
		t.Fatal("selection was not active after left selection")
	}

	screen.MarkOutput()
	sendTerminalMouse(app, tea.MouseButtonRight, tea.MouseActionPress, 4, 0)

	if app.terminal.hasSelection() {
		t.Fatal("selection should clear after output version changes")
	}
	if clip.copied != "" {
		t.Fatalf("copied stale selection %q, want no copy", clip.copied)
	}
	if len(wrote) != 0 {
		t.Fatalf("right-click wrote paste %q, want no paste after stale selection", string(wrote))
	}
	if !strings.Contains(app.status, "selection cleared") {
		t.Fatalf("status = %q, want stale selection notice", app.status)
	}
}

func TestTerminalCtrlCCopiesSelectionInsteadOfInterrupt(t *testing.T) {
	clip := &fakeClipboard{}
	app, _ := newMouseTerminalApp(t, clip)
	var wrote []byte
	app.terminal.writeInput = func(input []byte) error {
		wrote = append(wrote, input...)
		return nil
	}

	sendTerminalMouse(app, tea.MouseButtonLeft, tea.MouseActionPress, 0, 0)
	sendTerminalMouse(app, tea.MouseButtonLeft, tea.MouseActionRelease, 4, 0)
	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyCtrlC})

	if clip.copied != "hello" {
		t.Fatalf("copied = %q, want hello", clip.copied)
	}
	if len(wrote) != 0 {
		t.Fatalf("ctrl-c wrote %q to PTY, want no interrupt while selection exists", string(wrote))
	}
}

func TestTerminalBareCtrlKeepsSelection(t *testing.T) {
	for _, msg := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{0}},
		{Type: tea.KeyNull},
	} {
		app, _ := newMouseTerminalApp(t, &fakeClipboard{})
		var wrote []byte
		app.terminal.writeInput = func(input []byte) error {
			wrote = append(wrote, input...)
			return nil
		}

		sendTerminalMouse(app, tea.MouseButtonLeft, tea.MouseActionPress, 0, 0)
		sendTerminalMouse(app, tea.MouseButtonLeft, tea.MouseActionRelease, 4, 0)
		_, _ = app.Update(msg)

		if !app.terminal.hasSelection() {
			t.Fatalf("%v cleared selection, want selection kept", msg.Type)
		}
		if len(wrote) != 0 {
			t.Fatalf("%v wrote %q to PTY, want no bytes", msg.Type, string(wrote))
		}
	}
}

func TestTerminalTypingClearsSelection(t *testing.T) {
	app, _ := newMouseTerminalApp(t, &fakeClipboard{})
	var wrote []byte
	app.terminal.writeInput = func(input []byte) error {
		wrote = append(wrote, input...)
		return nil
	}

	sendTerminalMouse(app, tea.MouseButtonLeft, tea.MouseActionPress, 0, 0)
	sendTerminalMouse(app, tea.MouseButtonLeft, tea.MouseActionRelease, 4, 0)
	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})

	if app.terminal.hasSelection() {
		t.Fatal("typing should clear selection")
	}
	if string(wrote) != "x" {
		t.Fatalf("typed bytes = %q, want x", string(wrote))
	}
}

func TestTerminalRightPasteWhenNoSelection(t *testing.T) {
	clip := &fakeClipboard{paste: "a\r\nb"}
	app, _ := newMouseTerminalApp(t, clip)
	var wrote []byte
	app.terminal.writeInput = func(input []byte) error {
		wrote = append(wrote, input...)
		return nil
	}

	sendTerminalMouse(app, tea.MouseButtonRight, tea.MouseActionPress, 0, 0)

	if got := string(wrote); got != "a\rb" {
		t.Fatalf("paste wrote %q, want CR-normalized text", got)
	}
}

func TestNormalizePasteInputUsesPTYEnter(t *testing.T) {
	if got := normalizePasteInput("a\r\nb\nc\rd\n"); got != "a\rb\rc\rd" {
		t.Fatalf("normalizePasteInput = %q, want CR line endings without trailing submit", got)
	}
}

func TestTerminalBracketedPasteKeySendsNewlinesAsEnter(t *testing.T) {
	app := New(Options{Clipboard: &fakeClipboard{}})
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

	_, _ = app.Update(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune("ls \\\n-l\n"),
		Paste: true,
	})

	if got := string(wrote); got != "ls \\\r-l" {
		t.Fatalf("pasted bytes = %q, want trailing paste newline stripped", got)
	}
}

func TestTerminalRightPasteReportsClipboardError(t *testing.T) {
	app, _ := newMouseTerminalApp(t, &fakeClipboard{err: errors.New("missing clipboard")})

	sendTerminalMouse(app, tea.MouseButtonRight, tea.MouseActionPress, 0, 0)

	if !strings.Contains(app.err, "missing clipboard") {
		t.Fatalf("err = %q, want clipboard error", app.err)
	}
}

func TestTerminalDragSelectionAboveViewScrollsIntoHistory(t *testing.T) {
	clip := &fakeClipboard{}
	app := New(Options{Clipboard: clip})
	app.view = viewMain
	app.right = rightTerminal
	app.focus = focusRight
	app.width, app.height = 100, 10

	screen := sshterm.NewScreen(8)
	for i := 0; i < 20; i++ {
		for _, r := range fmt.Sprintf("L%02d", i) {
			screen.Print(r, sshterm.Style{})
		}
		screen.CarriageReturn()
		screen.LineFeed()
	}
	app.terminal = terminalModel{term: sshterm.NewTerminalWithScreen(screen)}

	_, h := app.RightSize()
	_ = screen.View(h)
	initialStart := screen.ViewStart(h)
	anchorRow := h - 2 // avoid the trailing blank line at the live bottom
	anchorAbs := initialStart + anchorRow

	sendTerminalMouse(app, tea.MouseButtonLeft, tea.MouseActionPress, 2, anchorRow)
	sendTerminalMouse(app, tea.MouseButtonLeft, tea.MouseActionMotion, 0, -1)
	for i := 0; i < 4; i++ {
		_, _ = app.Update(terminalRefreshMsg{})
	}
	if screen.ScrollOffset() == 0 {
		t.Fatal("dragging above the terminal did not scroll into history")
	}
	newStart := screen.ViewStart(h)
	sendTerminalMouse(app, tea.MouseButtonNone, tea.MouseActionRelease, 0, -1)
	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyCtrlC})

	if !strings.Contains(clip.copied, fmt.Sprintf("L%02d", newStart)) {
		t.Fatalf("copied selection %q does not include scrolled history row L%02d", clip.copied, newStart)
	}
	if !strings.Contains(clip.copied, fmt.Sprintf("L%02d", anchorAbs)) {
		t.Fatalf("copied selection %q does not include anchor row L%02d", clip.copied, anchorAbs)
	}
}
