package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"xcx/internal/sshterm"
)

// terminalModel owns an sshterm.Terminal attached to the active session.
type terminalModel struct {
	term    *sshterm.Terminal
	ctx     context.Context
	cancel  context.CancelFunc
	ticking bool
}

func newTerminalModel() terminalModel {
	return terminalModel{}
}

// openTerminalCmd starts the terminal once a session is attached. It returns a
// tea.Cmd that performs the PTY setup in a goroutine and emits a message.
func openTerminalCmd(app *App) tea.Cmd {
	return func() tea.Msg {
		tm := &app.terminal
		if app.sess == nil {
			return terminalErrorMsg{err: fmt.Errorf("no session")}
		}
		w, h := app.RawSize()
		sshSess, err := app.sess.Client().NewSession()
		if err != nil {
			return terminalErrorMsg{err: fmt.Errorf("new session: %w", err)}
		}
		term, err := sshterm.NewTerminal(sshSess, w, h)
		if err != nil {
			return terminalErrorMsg{err: err}
		}
		ctx, cancel := context.WithCancel(context.Background())
		tm.ctx, tm.cancel = ctx, cancel
		term.Start(ctx)
		tm.term = term
		tm.ticking = true
		return terminalStartedMsg{}
	}
}

type terminalStartedMsg struct{}
type terminalErrorMsg struct{ err error }
type terminalRefreshMsg struct{}
type terminalDoneMsg struct{}

func terminalRefresh() tea.Cmd {
	return tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg { return terminalRefreshMsg{} })
}

func waitForTerminalDone(t *sshterm.Terminal) tea.Cmd {
	return func() tea.Msg {
		<-t.Done()
		return terminalDoneMsg{}
	}
}

func (m terminalModel) Update(app *App, msg tea.Msg) (terminalModel, tea.Cmd) {
	switch msg := msg.(type) {
	case terminalStartedMsg:
		return m, tea.Batch(terminalRefresh(), waitForTerminalDone(m.term))
	case terminalErrorMsg:
		app.err = fmt.Sprintf("terminal: %v", msg.err)
		app.closeTerminal()
		app.view = viewHostTree
		return m, nil
	case terminalRefreshMsg:
		return m, terminalRefresh()
	case terminalDoneMsg:
		// Done() fires on three causes; report each accurately and only return
		// to the host tree when the shell truly ended (clean exit or real
		// error). We capture err before closeTerminal() clears the terminal.
		var termErr error
		if m.term != nil {
			termErr = m.term.Err()
		}
		app.closeTerminal()
		switch {
		case termErr == nil:
			// clean EOF: the remote shell exited (user typed exit, logout, etc.)
			app.status = "session ended"
			app.err = ""
		case errors.Is(termErr, context.Canceled), errors.Is(termErr, context.DeadlineExceeded):
			// cancelled by us (Ctrl+\ / disconnect): not an error
			app.status = "disconnected"
			app.err = ""
		default:
			app.status = "session ended"
			app.err = fmt.Sprintf("terminal: %v", termErr)
		}
		app.view = viewHostTree
		return m, nil
	case tea.WindowSizeMsg:
		if m.term != nil {
			w, h := app.RawSize()
			_ = m.term.Resize(w, h)
		}
		return m, nil
	case tea.KeyMsg:
		// Ctrl+\ handled by the top-level app to leave the terminal.
		if msg.Type == tea.KeyCtrlBackslash {
			return m, nil
		}
		if m.term == nil {
			return m, nil
		}
		if err := m.term.WriteInput(encodeKey(msg)); err != nil {
			app.err = fmt.Sprintf("write: %v", err)
		}
		return m, nil
	}
	return m, nil
}

// View renders the current screen contents, applying each cell's SGR style
// (bold/reverse/colors/background) so styled spans like `top`'s reverse header
// row appear with their intended attributes.
func (m terminalModel) View(app *App) string {
	if m.term == nil {
		return dimStyle.Render("starting terminal…")
	}
	w, h := app.RawSize()
	screen := m.term.Screen()
	view := screen.View(h)
	curRow, curCol := screen.CursorInView(h)
	var b strings.Builder
	for r := 0; r < h; r++ {
		var row []sshterm.Cell
		if r < len(view) {
			row = view[r]
		}
		// Only the row matching the cursor gets the inverted cursor cell.
		cursorCol := -1
		if r == curRow && curCol >= 0 && curCol < w {
			cursorCol = curCol
		}
		s := renderRow(row, w, cursorCol)
		// trim to the visible width (renderRow already pads to w with spaces)
		if lipgloss.Width(s) > w {
			s = s[:w]
		}
		b.WriteString(s)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// close stops the terminal read loop and frees resources.
func (m *terminalModel) close() {
	if m.cancel != nil {
		m.cancel()
	}
	if m.term != nil {
		_ = m.term.Close()
	}
	m.term = nil
}

// encodeKey converts a bubbletea KeyMsg into the byte sequence the remote PTY
// expects (xterm input encoding for control/function/arrow keys).
func encodeKey(k tea.KeyMsg) []byte {
	// Plain runes: send as-is (UTF-8), but drop C0 control bytes that arrive as
	// runes on Windows. Windows reports a lone Ctrl press (and some Ctrl+key
	// combos that have no dedicated control code) as KeyRunes with a NUL
	// ('\x00') rune, because the console event's Char is 0 when Ctrl produces
	// no printable character. Forwarding that NUL writes 0x00 to the PTY, which
	// shells echo as "^@" — one per Ctrl press. We strip NUL and other C0
	// controls so these spurious presses send nothing.
	if k.Type == tea.KeyRunes {
		var kept []rune
		for _, r := range k.Runes {
			if r >= 0x20 || r == '\t' {
				kept = append(kept, r)
			}
		}
		return []byte(string(kept))
	}
	// Lone Ctrl (KeyNull/KeyCtrlAt on Unix): a bare Ctrl press with no other
	// key should not inject a NUL into the remote stream. Drop it.
	if k.Type == tea.KeyNull {
		return nil
	}
	switch k.Type {
	case tea.KeyEnter:
		return []byte{'\r'}
	case tea.KeyBackspace:
		return []byte{0x7f}
	case tea.KeyTab:
		return []byte{'\t'}
	case tea.KeySpace:
		return []byte{' '}
	case tea.KeyEsc:
		return []byte{0x1b}
	case tea.KeyUp:
		return []byte{0x1b, '[', 'A'}
	case tea.KeyDown:
		return []byte{0x1b, '[', 'B'}
	case tea.KeyRight:
		return []byte{0x1b, '[', 'C'}
	case tea.KeyLeft:
		return []byte{0x1b, '[', 'D'}
	case tea.KeyHome:
		return []byte{0x1b, '[', 'H'}
	case tea.KeyEnd:
		return []byte{0x1b, '[', 'F'}
	case tea.KeyDelete:
		return []byte{0x1b, '[', '3', '~'}
	case tea.KeyPgUp:
		return []byte{0x1b, '[', '5', '~'}
	case tea.KeyPgDown:
		return []byte{0x1b, '[', '6', '~'}
	}
	// Ctrl+letter => control code.
	if k.Type >= tea.KeyCtrlA && k.Type <= tea.KeyCtrlZ {
		return []byte{byte(k.Type - tea.KeyCtrlA + 1)}
	}
	return nil
}
