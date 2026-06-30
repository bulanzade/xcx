package ui

import (
	"errors"
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

func TestTerminalRightPasteWhenNoSelection(t *testing.T) {
	clip := &fakeClipboard{paste: "a\r\nb"}
	app, _ := newMouseTerminalApp(t, clip)
	var wrote []byte
	app.terminal.writeInput = func(input []byte) error {
		wrote = append(wrote, input...)
		return nil
	}

	sendTerminalMouse(app, tea.MouseButtonRight, tea.MouseActionPress, 0, 0)

	if got := string(wrote); got != "a\nb" {
		t.Fatalf("paste wrote %q, want normalized text", got)
	}
}

func TestTerminalRightPasteReportsClipboardError(t *testing.T) {
	app, _ := newMouseTerminalApp(t, &fakeClipboard{err: errors.New("missing clipboard")})

	sendTerminalMouse(app, tea.MouseButtonRight, tea.MouseActionPress, 0, 0)

	if !strings.Contains(app.err, "missing clipboard") {
		t.Fatalf("err = %q, want clipboard error", app.err)
	}
}
