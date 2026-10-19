package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	// fwdMousePress is true between a mouse press we forwarded to the remote
	// program and its release. Releases arriving outside the terminal pane
	// (drag onto a pane border, release over another panel) must still be
	// forwarded so remote selections and drags complete instead of leaving
	// the remote program with a button it never sees released.
	fwdMousePress bool
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
	dragRow   int
	dragCol   int
	scroll    int
}

type terminalMouseMsg struct {
	msg    tea.MouseMsg
	row    int
	col    int
	inside bool
	scroll int
}

const (
	terminalSelectionRowEnd     = 1 << 30
	terminalSelectionScrollStep = 1
)

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
		var opts []sshterm.TerminalOption
		if localTrueColor() {
			// Forward the local terminal's 24-bit capability so remote TUIs
			// (codex, pi, …) emit their native palette instead of downsampling
			// to 256 colors. Best-effort: sshd's AcceptEnv gates env requests.
			opts = append(opts, sshterm.WithEnv("COLORTERM", "truecolor"))
		}
		term, err := sshterm.NewTerminal(sshSess, w, h, opts...)
		if err != nil {
			return terminalErrorMsg{sess: sess, err: err}
		}
		ctx, cancel := context.WithCancel(context.Background())
		term.Start(ctx)
		return terminalStartedMsg{sess: sess, key: connKey, term: term, ctx: ctx, cancel: cancel}
	}
}

// localTrueColor reports whether the terminal xcx runs in advertised 24-bit
// color support (COLORTERM=truecolor/24bit, set by Windows Terminal, iTerm2,
// kitty, alacritty, wezterm, VTE-based terminals, …). Inside tmux without
// pass-through config COLORTERM is absent, correctly downgrading the remote
// side to TERM=xterm-256color.
func localTrueColor() bool {
	switch os.Getenv("COLORTERM") {
	case "truecolor", "24bit":
		return true
	}
	return false
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
		// RightSize() shrank by the terminal pane's border (2 cols/rows) when
		// app.right switched to rightTerminal just above — but openTerminalCmd
		// captured the size BEFORE the switch. Sync the corrected size to the
		// PTY and screen now; without this the PTY stayed wider than the render
		// area and full-width box borders (pi's input box) were clipped.
		w, h := app.RightSize()
		_ = m.term.Resize(w, h)
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
		m.scrollSelectionDrag(app)
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
			// bubbletea decodes vim's application-cursor arrow key ESC O A as
			// KeyShiftUp. In application cursor mode the key belongs to the
			// remote program (vim/less/man navigation) and must be forwarded in
			// the application encoding; only in the normal shell do we use it
			// for scroll-back.
			if m.applicationCursor() {
				if err := m.write([]byte{0x1b, 'O', 'A'}); err != nil {
					app.err = fmt.Sprintf("write: %v", err)
				}
				return m, nil
			}
			m.clearSelection()
			m.scroll(1)
			return m, nil
		case tea.KeyShiftDown:
			if m.applicationCursor() {
				if err := m.write([]byte{0x1b, 'O', 'B'}); err != nil {
					app.err = fmt.Sprintf("write: %v", err)
				}
				return m, nil
			}
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
		input := encodeKey(msg, m.applicationCursor())
		if len(input) == 0 {
			return m, nil
		}
		m.clearSelection()
		if err := m.write(input); err != nil {
			app.err = fmt.Sprintf("write: %v", err)
		}
		return m, nil
	}
	return m, nil
}

func (m terminalModel) handleMouse(app *App, msg terminalMouseMsg) (terminalModel, tea.Cmd) {
	// While the remote program owns the mouse (it enabled xterm mouse
	// reporting: htop's setup dialogs, vim, tmux), forward events to it
	// instead of driving local selection/scrollback. SHIFT bypasses the app,
	// matching xterm, so the wheel still scrolls local history.
	if cfg := m.mouseConfig(); cfg.Reporting && !msg.msg.Shift {
		if msg.inside {
			if msg.msg.Action == tea.MouseActionPress && isPlainButton(msg.msg.Button) {
				m.fwdMousePress = true
			}
			m.forwardMouse(app, msg, cfg)
			if msg.msg.Action == tea.MouseActionRelease {
				m.fwdMousePress = false
			}
			return m, nil
		}
		if m.fwdMousePress && msg.msg.Action == tea.MouseActionRelease {
			// A press we forwarded must not strand the remote program: drag
			// out of the pane and release still forwards the release, with
			// the pane-clamped coordinates forwardMouse computes.
			m.fwdMousePress = false
			m.forwardMouse(app, msg, cfg)
			return m, nil
		}
	} else {
		// Reporting off (or SHIFT held): local handling owns the mouse now;
		// don't let a press from before the switch look unreleased.
		m.fwdMousePress = false
	}
	switch msg.msg.Button {
	case tea.MouseButtonWheelUp:
		if msg.inside && msg.msg.Action == tea.MouseActionPress {
			m.scroll(3)
		}
		return m, nil
	case tea.MouseButtonWheelDown:
		if msg.inside && msg.msg.Action == tea.MouseActionPress {
			m.scroll(-3)
		}
		return m, nil
	case tea.MouseButtonLeft, tea.MouseButtonNone:
		switch msg.msg.Action {
		case tea.MouseActionPress:
			if msg.msg.Button == tea.MouseButtonLeft && msg.inside {
				p := m.selectionPoint(app, msg.row, msg.col)
				m.selection = terminalSelection{
					selecting: true,
					active:    true,
					version:   m.outputVersion(),
					anchor:    p,
					cursor:    p,
					dragRow:   msg.row,
					dragCol:   msg.col,
				}
			} else {
				m.clearSelection()
			}
		case tea.MouseActionMotion:
			if m.selection.selecting {
				m.selection.dragRow = msg.row
				m.selection.dragCol = msg.col
				m.selection.scroll = msg.scroll
				m.scrollSelectionDrag(app)
				m.selection.active = true
			}
		case tea.MouseActionRelease:
			if m.selection.selecting {
				m.selection.dragRow = msg.row
				m.selection.dragCol = msg.col
				m.selection.scroll = 0
				m.selection.cursor = m.selectionPoint(app, msg.row, msg.col)
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

// mouseConfig returns the mouse reporting state the remote program requested,
// zero value when no terminal is attached.
func (m terminalModel) mouseConfig() sshterm.MouseConfig {
	if m.term == nil {
		return sshterm.MouseConfig{}
	}
	return m.term.MouseConfig()
}

// forwardMouse encodes one mouse event in the encoding the remote program
// asked for (SGR "CSI < b;x;y M/m" with ?1006, otherwise the classic 3-byte
// "CSI M" form) and writes it to the PTY. col/row are 0-based pane cells;
// xterm mouse coordinates are 1-based.
func (m terminalModel) forwardMouse(app *App, msg terminalMouseMsg, cfg sshterm.MouseConfig) {
	e := msg.msg
	if e.Action == tea.MouseActionMotion {
		if cfg.Motion == 0 || (cfg.Motion == 1 && e.Button == tea.MouseButtonNone) {
			return
		}
	}
	if e.Action == tea.MouseActionRelease && cfg.X10 {
		// The X10 protocol reports presses only — in any encoding, SGR
		// included; letting releases through under ?1006 gave press-only
		// applications events they never asked for.
		return
	}

	cb := mouseButtonCode(e)
	if e.Action == tea.MouseActionMotion {
		cb |= 32
	}
	if e.Alt {
		cb |= 8
	}
	if e.Ctrl {
		cb |= 16
	}
	// Both classic encodings (?1005 and the plain byte form) mark a release
	// by forcing the button bits to 11 while keeping the modifiers; SGR
	// instead uses the 'm' final byte. Skipping this under ?1005 made every
	// release decode as another press of the same button.
	if !cfg.SGR && e.Action == tea.MouseActionRelease {
		cb = cb&^3 | 3
	}

	col, row := msg.col+1, msg.row+1
	if col < 1 {
		col = 1
	}
	if row < 1 {
		row = 1
	}

	var b []byte
	if cfg.SGR {
		final := byte('M')
		if e.Action == tea.MouseActionRelease && e.Button != tea.MouseButtonWheelUp && e.Button != tea.MouseButtonWheelDown {
			final = 'm'
		}
		b = []byte(fmt.Sprintf("\x1b[<%d;%d;%d%c", cb, col, row, final))
	} else if cfg.UTF8 {
		// ?1005: the classic form with coordinates encoded as UTF-8 runes so
		// panes beyond column 95 still report; the button byte stays ASCII.
		b = []byte("\x1b[M" + string([]rune{rune(32 + cb), rune(32 + col), rune(32 + row)}))
	} else {
		// Plain classic X10 form: coordinates are raw bytes biased by 32 and
		// cap at 223 (byte 255).
		if col > 223 {
			col = 223
		}
		if row > 223 {
			row = 223
		}
		b = []byte{'\x1b', '[', 'M', byte(32 + cb), byte(32 + col), byte(32 + row)}
	}
	if err := m.write(b); err != nil {
		app.err = fmt.Sprintf("mouse: %v", err)
	}
}

// isPlainButton reports whether b is a real mouse button (not wheel or
// none): only plain buttons have a release event to remember between the
// press and its release.
func isPlainButton(b tea.MouseButton) bool {
	switch b {
	case tea.MouseButtonLeft, tea.MouseButtonMiddle, tea.MouseButtonRight:
		return true
	}
	return false
}

// mouseButtonCode maps a bubbletea button to the xterm mouse button number
// (0=left, 1=middle, 2=right, 3=none/motion, 64-67=wheel).
func mouseButtonCode(e tea.MouseMsg) int {
	switch e.Button {
	case tea.MouseButtonLeft:
		return 0
	case tea.MouseButtonMiddle:
		return 1
	case tea.MouseButtonRight:
		return 2
	case tea.MouseButtonWheelUp:
		return 64
	case tea.MouseButtonWheelDown:
		return 65
	case tea.MouseButtonWheelLeft:
		return 66
	case tea.MouseButtonWheelRight:
		return 67
	default:
		return 3
	}
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
	return m.term.Screen().TextRangeAbs(
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

func (m terminalModel) selectionPoint(app *App, row, col int) terminalPoint {
	if m.term == nil {
		return terminalPoint{row: row, col: col}
	}
	_, h := app.RightSize()
	return terminalPoint{row: m.term.Screen().ViewStart(h) + row, col: col}
}

func (m *terminalModel) scrollSelectionDrag(app *App) {
	if !m.selection.selecting || m.selection.scroll == 0 || m.term == nil {
		if m.selection.selecting {
			m.selection.cursor = m.selectionPoint(app, m.selection.dragRow, m.selection.dragCol)
		}
		return
	}
	m.scroll(m.selection.scroll * terminalSelectionScrollStep)
	m.selection.cursor = m.selectionPoint(app, m.selection.dragRow, m.selection.dragCol)
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

// applicationCursor reports whether the remote program enabled DECCKM
// application cursor keys (vim/less/man). When true, arrow keys must be
// forwarded to the remote program rather than used for local scroll-back.
func (m terminalModel) applicationCursor() bool {
	return m.term != nil && m.term.ApplicationCursor()
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
	viewStart := screen.ViewStart(h)
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
		selStart, selEnd := m.selectionRangeForRow(viewStart + r)
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
// expects (xterm input encoding for control/function/arrow keys). When
// appCursor is true the remote program enabled DECCKM application cursor keys
// (e.g. vim), so arrow keys are emitted in the application encoding (ESC O A)
// rather than the normal-cursor encoding (ESC [ A) — otherwise vim ignores them.
func encodeKey(k tea.KeyMsg, appCursor bool) []byte {
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
		return arrowKey(appCursor, 'A')
	case tea.KeyDown:
		return arrowKey(appCursor, 'B')
	case tea.KeyRight:
		return arrowKey(appCursor, 'C')
	case tea.KeyLeft:
		return arrowKey(appCursor, 'D')
	case tea.KeyHome:
		// Home/End don't have a separate application-mode encoding in common
		// use; keep the normal-cursor sequence.
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

// arrowKey returns the encoding for an arrow key (final byte A/B/C/D): ESC O x
// in application-cursor mode (DECCKM on, e.g. vim), ESC [ x otherwise. Programs
// that enable application cursor keys only recognize the ESC O form.
func arrowKey(appCursor bool, final byte) []byte {
	if appCursor {
		return []byte{0x1b, 'O', final}
	}
	return []byte{0x1b, '[', final}
}
