package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"xcx/internal/session"
	"xcx/internal/sshterm"
)

// terminalModel owns an sshterm.Terminal attached to the active session.
type terminalModel struct {
	term       *sshterm.Terminal
	ctx        context.Context
	cancel     context.CancelFunc
	ticking    bool
	writeInput func([]byte) error
	selection  terminalSelection
}

func newTerminalModel() terminalModel {
	return terminalModel{}
}

type terminalPoint struct {
	row int
	col int
}

type terminalSelection struct {
	selecting bool
	active    bool
	version   uint64
	anchor    terminalPoint
	cursor    terminalPoint
}

type terminalMouseMsg struct {
	msg    tea.MouseMsg
	row    int
	col    int
	inside bool
}

const terminalSelectionRowEnd = 1 << 30

// openTerminalCmd starts the terminal once a session is attached. It returns a
// tea.Cmd that performs the PTY setup in a goroutine and emits a message.
func openTerminalCmd(app *App, sess *session.Session, connKey string) tea.Cmd {
	w, h := app.RightSize()
	return func() tea.Msg {
		if sess == nil {
			return terminalErrorMsg{err: fmt.Errorf("no session")}
		}
		sshSess, err := sess.Client().NewSession()
		if err != nil {
			return terminalErrorMsg{sess: sess, err: fmt.Errorf("new session: %w", err)}
		}
		term, err := sshterm.NewTerminal(sshSess, w, h)
		if err != nil {
			return terminalErrorMsg{sess: sess, err: err}
		}
		ctx, cancel := context.WithCancel(context.Background())
		term.Start(ctx)
		return terminalStartedMsg{sess: sess, key: connKey, term: term, ctx: ctx, cancel: cancel}
	}
}

type terminalStartedMsg struct {
	sess   *session.Session
	key    string
	term   *sshterm.Terminal
	ctx    context.Context
	cancel context.CancelFunc
}
type terminalErrorMsg struct {
	sess *session.Session
	err  error
}
type terminalRefreshMsg struct{}
type terminalDoneMsg struct{ term *sshterm.Terminal }

func terminalRefresh() tea.Cmd {
	return tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg { return terminalRefreshMsg{} })
}

func waitForTerminalDone(t *sshterm.Terminal) tea.Cmd {
	return func() tea.Msg {
		<-t.Done()
		return terminalDoneMsg{term: t}
	}
}

func (m terminalModel) Update(app *App, msg tea.Msg) (terminalModel, tea.Cmd) {
	switch msg := msg.(type) {
	case terminalStartedMsg:
		app.storeActiveTerminal()
		m.term = msg.term
		m.ctx = msg.ctx
		m.cancel = msg.cancel
		m.ticking = true
		app.sess = msg.sess
		app.activeHostKey = msg.key
		if app.activeHostKey == "" {
			app.activeHostKey = hostConnKey(msg.sess.Host)
		}
		app.ensureSessionMaps()
		app.sessions[app.activeHostKey] = msg.sess
		app.terminals[app.activeHostKey] = m
		app.status = fmt.Sprintf("connected to %s", msg.sess.Host.Name)
		app.err = ""
		app.right = rightTerminal
		app.focus = focusRight
		if m.term == nil {
			return m, nil
		}
		return m, tea.Batch(terminalRefresh(), waitForTerminalDone(m.term))
	case terminalErrorMsg:
		app.err = fmt.Sprintf("terminal: %v", msg.err)
		if msg.sess != nil && app.sess != msg.sess {
			_ = msg.sess.Close()
		} else if app.sess == msg.sess {
			app.closeTerminal()
		}
		return m, nil
	case terminalRefreshMsg:
		m.clearSelectionIfOutputChanged()
		return m, terminalRefresh()
	case terminalDoneMsg:
		if msg.term != nil && m.term != msg.term {
			app.removeBackgroundTerminal(msg.term)
			return m, nil
		}
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
		return m, nil
	case tea.WindowSizeMsg:
		if m.term != nil {
			w, h := app.RightSize()
			_ = m.term.Resize(w, h)
		}
		return m, nil
	case terminalMouseMsg:
		if m.clearSelectionIfOutputChanged() && msg.msg.Button == tea.MouseButtonRight && msg.msg.Action == tea.MouseActionPress {
			app.status = "selection cleared after output"
			app.err = ""
			return m, nil
		}
		return m.handleMouse(app, msg)
	case tea.KeyMsg:
		if m.term == nil && m.writeInput == nil {
			return m, nil
		}
		if msg.Paste {
			m.clearSelection()
			if err := m.writePaste(string(msg.Runes)); err != nil {
				app.err = fmt.Sprintf("paste: %v", err)
			}
			return m, nil
		}
		switch msg.Type {
		case tea.KeyCtrlC:
			if m.hasSelection() {
				m.copySelection(app)
				m.clearSelection()
				return m, nil
			}
		case tea.KeyCtrlBackslash:
			app.closeTerminal()
			return m, nil
		case tea.KeyCtrlS:
			app.openSFTPFromTerminal()
			return m, nil
		case tea.KeyShiftUp:
			m.clearSelection()
			m.scroll(1)
			return m, nil
		case tea.KeyShiftDown:
			m.clearSelection()
			m.scroll(-1)
			return m, nil
		case tea.KeyPgUp, tea.KeyPgDown:
			// Always page through local scrollback. We deliberately do NOT
			// forward PgUp/PgDn to the remote shell: bash readline binds them
			// to history search, so forwarding made the command line flip
			// between history entries instead of scrolling — the opposite of
			// what the user expects. Full-screen apps (less/vim/man) don't
			// rely on these under our local terminal.
			_, h := app.RightSize()
			delta := h
			if msg.Type == tea.KeyPgDown {
				delta = -h
			}
			m.clearSelection()
			m.scroll(delta)
			return m, nil
		}
		// g / G jump to the top / bottom of scrollback (vim-style), but ONLY
		// while reviewing history — in live mode they must reach the shell so
		// the user can still type them (git, go build, …). We use these
		// instead of Ctrl+PgUp/PgDn because bubbletea's Windows console-input
		// layer cannot distinguish those from plain PgUp/PgDn (VK_PRIOR/
		// VK_NEXT map to KeyPgUp/KeyPgDown with no modifier), so the Ctrl
		// variants never arrived on Windows.
		if msg.Type == tea.KeyRunes && m.scrolledBack() {
			if len(msg.Runes) == 1 {
				switch msg.Runes[0] {
				case 'g':
					m.clearSelection()
					m.jumpTop()
					return m, nil
				case 'G':
					m.clearSelection()
					m.jumpBottom()
					return m, nil
				}
			}
		}
		m.clearSelection()
		if err := m.write(encodeKey(msg)); err != nil {
			app.err = fmt.Sprintf("write: %v", err)
		}
		return m, nil
	}
	return m, nil
}

func (m terminalModel) handleMouse(app *App, msg terminalMouseMsg) (terminalModel, tea.Cmd) {
	switch msg.msg.Button {
	case tea.MouseButtonWheelUp:
		if msg.inside && msg.msg.Action == tea.MouseActionPress {
			m.clearSelection()
			m.scroll(3)
		}
		return m, nil
	case tea.MouseButtonWheelDown:
		if msg.inside && msg.msg.Action == tea.MouseActionPress {
			m.clearSelection()
			m.scroll(-3)
		}
		return m, nil
	case tea.MouseButtonLeft, tea.MouseButtonNone:
		switch msg.msg.Action {
		case tea.MouseActionPress:
			if msg.msg.Button == tea.MouseButtonLeft && msg.inside {
				p := terminalPoint{row: msg.row, col: msg.col}
				m.selection = terminalSelection{
					selecting: true,
					active:    true,
					version:   m.outputVersion(),
					anchor:    p,
					cursor:    p,
				}
			} else {
				m.clearSelection()
			}
		case tea.MouseActionMotion:
			if m.selection.selecting {
				m.selection.cursor = terminalPoint{row: msg.row, col: msg.col}
				m.selection.active = true
			}
		case tea.MouseActionRelease:
			if m.selection.selecting {
				m.selection.cursor = terminalPoint{row: msg.row, col: msg.col}
				m.selection.selecting = false
				if m.selectedText(app) == "" {
					m.clearSelection()
				}
			}
		}
		return m, nil
	case tea.MouseButtonRight:
		if msg.msg.Action != tea.MouseActionPress {
			return m, nil
		}
		if !msg.inside {
			return m, nil
		}
		if m.hasSelection() {
			m.copySelection(app)
			m.clearSelection()
			return m, nil
		}
		m.pasteClipboard(app)
		return m, nil
	}
	return m, nil
}

func (m terminalModel) write(input []byte) error {
	if m.writeInput != nil {
		return m.writeInput(input)
	}
	return m.term.WriteInput(input)
}

func (m terminalModel) writePaste(input string) error {
	return m.write([]byte(normalizePasteInput(input)))
}

func normalizePasteInput(input string) string {
	input = strings.ReplaceAll(input, "\r\n", "\n")
	input = strings.ReplaceAll(input, "\r", "\n")
	input = strings.TrimRight(input, "\n")
	return strings.ReplaceAll(input, "\n", "\r")
}

func (m terminalModel) pasteClipboard(app *App) {
	text, err := app.clip.Paste()
	if err != nil {
		app.err = fmt.Sprintf("paste: %v", err)
		return
	}
	if text == "" {
		app.status = "nothing to paste"
		app.err = ""
		return
	}
	if err := m.writePaste(text); err != nil {
		app.err = fmt.Sprintf("paste: %v", err)
		return
	}
	app.status = fmt.Sprintf("pasted %d chars", len([]rune(text)))
	app.err = ""
}

func (m terminalModel) copySelection(app *App) {
	text := m.selectedText(app)
	if text == "" {
		app.status = "nothing selected"
		app.err = ""
		return
	}
	method, err := app.clip.Copy(text)
	if err != nil {
		app.err = fmt.Sprintf("copy: %v", err)
		return
	}
	if method == "" {
		method = "clipboard"
	}
	app.status = fmt.Sprintf("copied %d chars via %s", len([]rune(text)), method)
	app.err = ""
}

func (m terminalModel) selectedText(app *App) string {
	if !m.hasSelection() || m.term == nil {
		return ""
	}
	if m.selection.version != m.outputVersion() {
		return ""
	}
	_, h := app.RightSize()
	return m.term.Screen().TextRange(h,
		sshterm.Point{Row: m.selection.anchor.row, Col: m.selection.anchor.col},
		sshterm.Point{Row: m.selection.cursor.row, Col: m.selection.cursor.col},
	)
}

func (m terminalModel) hasSelection() bool {
	return m.selection.active
}

func (m *terminalModel) clearSelection() {
	m.selection = terminalSelection{}
}

func (m terminalModel) outputVersion() uint64 {
	if m.term == nil {
		return 0
	}
	return m.term.Screen().OutputVersion()
}

func (m *terminalModel) clearSelectionIfOutputChanged() bool {
	if m.hasSelection() && m.selection.version != m.outputVersion() {
		m.clearSelection()
		return true
	}
	return false
}

// scroll moves the terminal view by delta rows (positive = up into history).
// Only meaningful when a real terminal is attached; for the writeInput path
// (no screen) it's a no-op.
func (m terminalModel) scroll(delta int) {
	if m.term != nil {
		m.term.Scroll(delta)
	}
}

// scrolledBack reports whether the user is currently reviewing scrollback
// history (the view offset is above the live bottom). Used to decide whether
// to intercept g/G for jumping or let them reach the shell.
func (m terminalModel) scrolledBack() bool {
	return m.term != nil && m.term.Screen().ScrollOffset() > 0
}

// jumpTop scrolls the view to the very top of scrollback.
func (m terminalModel) jumpTop() {
	if m.term != nil {
		m.term.Scroll(1 << 20) // clamped by the screen to scrollMax()
	}
}

// jumpBottom returns the view to live (bottom) output.
func (m terminalModel) jumpBottom() {
	if m.term != nil {
		m.term.Screen().ResetScroll()
	}
}

func (m terminalModel) available() bool {
	return m.term != nil || m.writeInput != nil
}

// View renders the current screen contents, applying each cell's SGR style
// (bold/reverse/colors/background) so styled spans like `top`'s reverse header
// row appear with their intended attributes.
func (m terminalModel) View(app *App) string {
	if m.term == nil {
		return dimStyle.Render("starting terminal…")
	}
	m.clearSelectionIfOutputChanged()
	w, h := app.RightSize()
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
		selStart, selEnd := m.selectionRangeForRow(r)
		s := renderRow(row, w, cursorCol, selStart, selEnd)
		b.WriteString(s)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m terminalModel) selectionRangeForRow(row int) (int, int) {
	if !m.hasSelection() {
		return -1, -1
	}
	start, end := m.selection.anchor, m.selection.cursor
	if afterPoint(start, end) {
		start, end = end, start
	}
	if row < start.row || row > end.row {
		return -1, -1
	}
	switch {
	case start.row == end.row:
		return start.col, end.col
	case row == start.row:
		return start.col, terminalSelectionRowEnd
	case row == end.row:
		return 0, end.col
	default:
		return 0, terminalSelectionRowEnd
	}
}

func afterPoint(a, b terminalPoint) bool {
	if a.row != b.row {
		return a.row > b.row
	}
	return a.col > b.col
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
