package ui

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"xcx/internal/session"
	"xcx/internal/sshterm"
	"xcx/internal/transfer"
	"xcx/internal/vault"
)

// view is the active screen of the app.
type view int

const (
	viewUnlock        view = iota
	viewHostKeyPrompt      // modal-ish: confirm an unknown host key
	viewEdit               // add/edit a host
	viewMain               // persistent split layout
)

// rightPane is the active panel on the right side of the split view.
type rightPane int

const (
	rightPlaceholder rightPane = iota
	rightTerminal
	rightSFTP
)

// focus identifies which split pane receives keyboard input.
type focus int

const (
	focusLeft focus = iota
	focusRight
)

// Options configure how the App is built (paths, callbacks for vault ops).
type Options struct {
	// VaultPath is the encrypted config file path.
	VaultPath string
	// KnownHostsPath is the known_hosts file path.
	KnownHostsPath string
	// Clipboard overrides system clipboard access in tests.
	Clipboard appClipboard
}

// App is the top-level Bubble Tea model. It owns the active view and the
// shared services (vault, session manager, transfer queue) plus the current
// terminal width/height for child views.
type App struct {
	opts Options

	width, height int

	view  view
	right rightPane
	focus focus

	// services
	vault    *vault.Vault
	master   string // current master password (needed to Save)
	verifier *session.HostKeyVerifier
	queue    *transfer.Queue
	clip     appClipboard

	// active connection
	sess          *session.Session
	activeHostKey string
	activeSFTPKey string
	sessions      map[string]*session.Session
	terminals     map[string]terminalModel

	// child view state
	unlock   unlockModel
	hostTree hostTreeModel
	terminal terminalModel
	sftp     sftpModel
	hostKey  hostKeyModel
	edit     editModel

	// transient status line content
	status   string
	err      string
	statusMu sync.RWMutex
	transfer transferStatus
}

type transferStatus struct {
	active    bool
	label     string
	done      int64
	total     int64
	queueLeft int
	started   time.Time
	updated   time.Time
	speedBps  float64
	err       string
}

// New returns a new App initialized for the unlock screen.
func New(opts Options) *App {
	u := newUnlockModel()
	u.detectCreating(opts)
	a := &App{
		opts:   opts,
		view:   viewUnlock,
		queue:  transfer.NewQueue(1),
		unlock: u,
	}
	if opts.Clipboard != nil {
		a.clip = opts.Clipboard
	} else {
		a.clip = systemClipboard{}
	}
	return a
}

// Init starts the program: nothing to do until unlock succeeds.
func (a *App) Init() tea.Cmd { return nil }

// Update routes messages to the active view and handles global keys.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.propagateSize()
		if a.view == viewMain {
			_, cmd := a.dispatchRight(msg)
			return a, cmd
		}
		return a, nil

	case dialResultMsg:
		// An async SSH dial completed (terminal or SFTP). Route the result
		// without blocking — the dial already ran in a goroutine via dialCmd.
		return a, a.handleDialResult(msg)

	case terminalStartedMsg, terminalErrorMsg, terminalDoneMsg, terminalRefreshMsg:
		var cmd tea.Cmd
		a.terminal, cmd = a.terminal.Update(a, msg)
		return a, cmd

	case tea.MouseMsg:
		if a.view == viewMain {
			return a.handleMouse(msg)
		}
		return a, nil

	case tea.KeyMsg:
		if a.view != viewMain {
			return a.dispatchModal(msg)
		}

		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyCtrlQ:
			if a.focus == focusRight && a.right == rightTerminal {
				return a.dispatchRight(msg)
			}
			return a.quit()
		case tea.KeyShiftTab:
			return a.handleShiftTab()
		case tea.KeyTab:
			return a.handleTab()
		}

		if a.focus == focusLeft {
			var cmd tea.Cmd
			a.hostTree, cmd = a.hostTree.Update(a, msg)
			return a, cmd
		}
		return a.dispatchRight(msg)
	}

	if a.view == viewMain {
		a.hostTree, _ = a.hostTree.Update(a, msg)
		_, cmd := a.dispatchRight(msg)
		return a, cmd
	}
	return a.dispatchModal(msg)
}

func (a *App) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if a.right != rightTerminal || a.terminal.term == nil {
		return a, nil
	}
	x, y, ok := a.terminalContentPoint(msg.X, msg.Y)
	if !ok && msg.Button != tea.MouseButtonLeft {
		return a, nil
	}
	var cmd tea.Cmd
	a.terminal, cmd = a.terminal.Update(a, terminalMouseMsg{
		msg:    msg,
		col:    x,
		row:    y,
		inside: ok,
	})
	return a, cmd
}

func (a *App) terminalContentPoint(x, y int) (int, int, bool) {
	leftW, _, _ := a.layout()
	w, h := a.RightSize()
	// The terminal pane uses a one-cell Lip Gloss border. RightSize() returns
	// the content box inside that frame, so mouse coordinates must skip the
	// left pane, the one-column split gap, and the terminal frame's top/left
	// cell before they can be interpreted as PTY cell coordinates.
	contentX := leftW + 2
	contentY := 1
	if x < contentX || x >= contentX+w || y < contentY || y >= contentY+h {
		return clampInt(x-contentX, 0, w-1), clampInt(y-contentY, 0, h-1), false
	}
	return x - contentX, y - contentY, true
}

func clampInt(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	switch {
	case v < lo:
		return lo
	case v > hi:
		return hi
	default:
		return v
	}
}

func (a *App) handleTab() (tea.Model, tea.Cmd) {
	if a.right == rightTerminal && a.focus == focusRight {
		return a.dispatchRight(tea.KeyMsg{Type: tea.KeyTab})
	}
	if a.right != rightSFTP {
		if a.focus == focusLeft {
			a.focus = focusRight
		} else {
			a.focus = focusLeft
		}
		return a, nil
	}
	if a.focus == focusLeft {
		a.focus = focusRight
		if a.sftp.local != nil {
			a.sftp.focused = a.sftp.local
		}
		return a, nil
	}
	if a.sftp.focused == a.sftp.remote {
		a.focus = focusLeft
		if a.sftp.local != nil {
			a.sftp.focused = a.sftp.local
		}
		return a, nil
	}
	var cmd tea.Cmd
	a.sftp, cmd = a.sftp.Update(a, tea.KeyMsg{Type: tea.KeyTab})
	return a, cmd
}

func (a *App) handleShiftTab() (tea.Model, tea.Cmd) {
	if a.view != viewMain {
		return a, nil
	}
	if a.focus == focusRight {
		a.focus = focusLeft
		return a, nil
	}
	a.focus = focusRight
	return a, nil
}

// dispatchModal routes messages to full-screen modal views.
func (a *App) dispatchModal(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok && (k.Type == tea.KeyCtrlC || k.Type == tea.KeyCtrlQ) {
		return a.quit()
	}
	var cmd tea.Cmd
	switch a.view {
	case viewUnlock:
		a.unlock, cmd = a.unlock.Update(a, msg)
	case viewHostKeyPrompt:
		a.hostKey, cmd = a.hostKey.Update(a, msg)
	case viewEdit:
		a.edit, cmd = a.edit.Update(a, msg)
	}
	return a, cmd
}

// dispatchRight routes messages to the active right pane.
func (a *App) dispatchRight(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch a.right {
	case rightTerminal:
		var cmd tea.Cmd
		a.terminal, cmd = a.terminal.Update(a, msg)
		return a, cmd
	case rightSFTP:
		var cmd tea.Cmd
		a.sftp, cmd = a.sftp.Update(a, msg)
		return a, cmd
	default:
		return a, nil
	}
}

// View renders the active screen.
func (a *App) View() string {
	switch a.view {
	case viewUnlock:
		return a.framedModal(a.unlock.View(a))
	case viewHostKeyPrompt:
		return a.framedModal(a.hostKey.View(a))
	case viewEdit:
		return a.framedModal(a.edit.View(a))
	default:
		return a.viewSplit()
	}
}

// viewSplit renders the persistent split: left host tree, right activity pane,
// and a full-width status bar.
func (a *App) viewSplit() string {
	leftW, _, paneH := a.layout()

	leftBody := a.hostTree.View(a)
	leftStyle := leftPaneStyle
	if a.focus == focusLeft {
		leftStyle = leftPaneActiveStyle
	}
	left := leftStyle.Width(max(1, leftW-2)).Height(max(1, paneH-2)).Render(leftBody)

	var right string
	switch a.right {
	case rightTerminal:
		style := rightPaneStyle
		if a.focus == focusRight {
			style = rightPaneActiveStyle
		}
		right = style.Width(max(1, a.RightOuterWidth()-2)).Height(max(1, paneH-2)).Render(a.terminal.View(a))
	case rightSFTP:
		right = a.sftp.View(a)
	default:
		right = a.placeholderView()
	}

	body := lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
	out := body
	if a.err != "" {
		out += "\n" + errorStyle.Render(a.err)
	}
	out += "\n" + a.statusBar()
	return out
}

func (a *App) placeholderView() string {
	w, h := a.RightSize()
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center,
		dimStyle.Render("Select a host and press Enter to connect."))
}

func (a *App) framedModal(body string) string {
	out := appFrame.Render(body)
	if a.err != "" {
		out += "\n" + errorStyle.Render(a.err)
	}
	out += "\n" + a.statusBar()
	return out
}

// statusBar renders the persistent bottom bar.
func (a *App) statusBar() string {
	a.statusMu.RLock()
	left := a.status
	ts := a.transfer
	a.statusMu.RUnlock()
	if ts.active {
		left = ts.String()
	}
	if left == "" {
		left = "xcx — TUI SSH manager"
	}
	right := ""
	if a.sess != nil && a.sess.Host != nil {
		h := a.sess.Host
		right = fmt.Sprintf("%s@%s", h.User, h.Name)
	}
	if pending := a.queue.Pending(); pending > 0 {
		if right != "" {
			right += "  "
		}
		right += fmt.Sprintf("transfers queued: %d", pending)
	}
	gap := 1
	fill := a.width - lipgloss.Width(statusBarStyle.Render(left)) - lipgloss.Width(statusBarStyle.Render(right)) - gap
	if fill < 1 {
		fill = 1
	}
	bar := left + " " + strings.Repeat(" ", fill) + " " + right
	return statusBarStyle.Render(bar)
}

func (s transferStatus) String() string {
	name := filepath.Base(s.label)
	if name == "." || name == string(filepath.Separator) || name == "" {
		name = s.label
	}
	if s.err != "" {
		return fmt.Sprintf("transfer failed %s: %s", name, s.err)
	}
	pct := 0
	if s.total > 0 {
		pct = int(float64(s.done) * 100 / float64(s.total))
		if pct > 100 {
			pct = 100
		}
	}
	queue := ""
	if s.queueLeft > 0 {
		queue = fmt.Sprintf(" (%d queued)", s.queueLeft)
	}
	return fmt.Sprintf("transferring %s %d%% %s/s%s", name, pct, formatBytes(int64(s.speedBps)), queue)
}

func (a *App) updateTransferProgress(p transfer.Progress, started time.Time) {
	now := time.Now()
	elapsed := now.Sub(started).Seconds()
	speed := 0.0
	if elapsed > 0 {
		speed = float64(p.Done) / elapsed
	}
	a.statusMu.Lock()
	a.transfer = transferStatus{
		active:    true,
		label:     p.Job.Src,
		done:      p.Done,
		total:     p.Total,
		queueLeft: p.QueueLeft,
		started:   started,
		updated:   now,
		speedBps:  speed,
	}
	a.statusMu.Unlock()
}

func (a *App) finishTransfer(c transfer.Completed) {
	a.statusMu.Lock()
	defer a.statusMu.Unlock()
	if c.Job.Status == transfer.StatusFailed {
		a.transfer = transferStatus{
			active: true,
			label:  c.Job.Src,
			err:    c.Job.Err,
		}
		return
	}
	a.transfer = transferStatus{}
	a.status = fmt.Sprintf("transferred %s", filepath.Base(c.Job.Src))
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n >= div*unit && exp < 4 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func (a *App) propagateSize() {
	// child views query width/height from the app directly via accessors.
}

func (a *App) closeTerminal() {
	key := a.currentHostKey()
	a.closeTerminalKey(key, true)
	a.sess = nil
	a.activeHostKey = ""
	a.terminal = newTerminalModel()
	a.right = rightPlaceholder
	a.focus = focusLeft
}

func (a *App) closeTerminalKey(key string, closeSession bool) {
	if key == "" {
		return
	}
	a.ensureSessionMaps()
	if key == a.currentHostKey() {
		a.terminal.close()
	} else if tm, ok := a.terminals[key]; ok {
		tm.close()
	}
	delete(a.terminals, key)
	if closeSession && key != a.activeSFTPKey {
		if sess := a.sessions[key]; sess != nil {
			_ = sess.Close()
		}
		if key == a.currentHostKey() && a.sess != nil {
			_ = a.sess.Close()
		}
		delete(a.sessions, key)
		if key == a.currentHostKey() {
			a.sess = nil
			a.activeHostKey = ""
		}
	}
}

func (a *App) closeSessionKey(key string) {
	if key == "" {
		return
	}
	if key == a.activeSFTPKey {
		a.sftp.close()
		a.activeSFTPKey = ""
		if a.right == rightSFTP {
			a.right = rightPlaceholder
			a.focus = focusLeft
		}
	}
	a.closeTerminalKey(key, true)
	if sess := a.sessions[key]; sess != nil {
		_ = sess.Close()
	}
	delete(a.sessions, key)
	if key == a.currentHostKey() {
		a.sess = nil
		a.activeHostKey = ""
		a.terminal = newTerminalModel()
	}
}

func (a *App) removeBackgroundTerminal(t *sshterm.Terminal) {
	if t == nil {
		return
	}
	a.ensureSessionMaps()
	for key, tm := range a.terminals {
		if tm.term == t {
			tm.close()
			delete(a.terminals, key)
			if key != a.activeSFTPKey {
				if sess := a.sessions[key]; sess != nil {
					_ = sess.Close()
				}
				delete(a.sessions, key)
			}
			return
		}
	}
}

func (a *App) shutdown() {
	a.sftp.close()
	a.activeSFTPKey = ""
	a.terminal.close()
	closed := map[*session.Session]bool{}
	if a.sess != nil {
		_ = a.sess.Close()
		closed[a.sess] = true
	}
	a.ensureSessionMaps()
	for _, tm := range a.terminals {
		tm.close()
	}
	for _, sess := range a.sessions {
		if sess != nil && !closed[sess] {
			_ = sess.Close()
			closed[sess] = true
		}
	}
	a.sessions = nil
	a.terminals = nil
	a.sess = nil
	a.activeHostKey = ""
	a.terminal = newTerminalModel()
}

func (a *App) quit() (tea.Model, tea.Cmd) {
	a.shutdown()
	return a, tea.Quit
}

// openSFTPFromTerminal opens the right-side SFTP pane using the current SSH
// session.
func (a *App) openSFTPFromTerminal() {
	if a.sess == nil {
		return
	}
	sm, err := newSFTPModel(a)
	if err != nil {
		a.err = fmt.Sprintf("sftp: %v", err)
		return
	}
	a.sftp = sm
	a.activeSFTPKey = a.currentHostKey()
	a.right = rightSFTP
	a.focus = focusRight
}

// --- accessors used by child views --------------------------------------

// Size returns the usable width/height (minus the frame) for framed views
// (host tree, SFTP, etc.).
func (a *App) Size() (int, int) {
	w := a.width - 4 // appFrame horizontal padding
	h := a.height - 5
	if w < 10 {
		w = 10
	}
	if h < 3 {
		h = 3
	}
	return w, h
}

// RawSize returns the full terminal width/height with no frame subtraction.
// The terminal view is rendered full-screen (it skips the frame and status
// bar), so its PTY setup, resize, and rendering must use the full size —
// otherwise the remote PTY is opened sized 76x19 on an 80x24 terminal and
// full-screen programs mis-size.
func (a *App) RawSize() (int, int) {
	w := a.width
	h := a.height
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	return w, h
}

// layout returns split pane sizes: left width, right width, and pane height.
func (a *App) layout() (int, int, int) {
	const (
		leftTarget = 36
		gap        = 1
		rightMin   = 30
		leftFloor  = 22
	)
	paneH := a.height - 1
	if paneH < 1 {
		paneH = 1
	}
	leftW := leftTarget
	rightW := a.width - leftW - gap
	if rightW < rightMin {
		leftW = a.width - gap - rightMin
		if leftW < leftFloor {
			leftW = leftFloor
		}
		rightW = a.width - leftW - gap
	}
	if leftW < 1 {
		leftW = 1
	}
	if rightW < 1 {
		rightW = 1
	}
	return leftW, rightW, paneH
}

// LeftSize returns the host-tree content size inside the left pane border.
func (a *App) LeftSize() (int, int) {
	leftW, _, paneH := a.layout()
	w := leftW - 2
	h := paneH - 2
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	return w, h
}

// RightSize returns the content size of the right activity pane.
func (a *App) RightSize() (int, int) {
	_, rightW, paneH := a.layout()
	if a.right == rightTerminal {
		rightW -= 2
		paneH -= 2
	}
	if rightW < 1 {
		rightW = 1
	}
	if paneH < 1 {
		paneH = 1
	}
	return rightW, paneH
}

// RightOuterWidth returns the full right pane width before any child border is
// applied.
func (a *App) RightOuterWidth() int {
	_, rightW, _ := a.layout()
	if rightW < 1 {
		return 1
	}
	return rightW
}

// Width returns the full terminal width.
func (a *App) Width() int { return a.width }

// Height returns the full terminal height.
func (a *App) Height() int { return a.height }

func (a *App) ensureSessionMaps() {
	if a.sessions == nil {
		a.sessions = map[string]*session.Session{}
	}
	if a.terminals == nil {
		a.terminals = map[string]terminalModel{}
	}
}

func hostConnKey(h *vault.Host) string {
	if h == nil {
		return ""
	}
	return fmt.Sprintf("%s|%s|%s|%d", h.Name, h.User, h.Addr, portOr22(h.Port))
}

func (a *App) storeActiveTerminal() {
	if a.sess == nil {
		return
	}
	if a.activeHostKey == "" {
		a.activeHostKey = hostConnKey(a.sess.Host)
	}
	if a.activeHostKey == "" {
		return
	}
	a.ensureSessionMaps()
	a.sessions[a.activeHostKey] = a.sess
	if a.terminal.available() {
		a.terminals[a.activeHostKey] = a.terminal
	}
}

func (a *App) currentHostKey() string {
	if a.activeHostKey != "" {
		return a.activeHostKey
	}
	if a.sess != nil {
		return hostConnKey(a.sess.Host)
	}
	return ""
}

func (a *App) sessionForHost(h *vault.Host) *session.Session {
	return a.sessionForKey(h, hostConnKey(h))
}

func (a *App) sessionForKey(h *vault.Host, key string) *session.Session {
	if key == "" {
		return nil
	}
	if a.sess != nil && (a.activeHostKey == key || (h != nil && hostConnKey(a.sess.Host) == hostConnKey(h))) {
		return a.sess
	}
	a.ensureSessionMaps()
	return a.sessions[key]
}

func (a *App) keyForSession(sess *session.Session) string {
	if sess == nil {
		return ""
	}
	if a.sess == sess && a.activeHostKey != "" {
		return a.activeHostKey
	}
	a.ensureSessionMaps()
	for key, cached := range a.sessions {
		if cached == sess {
			return key
		}
	}
	return hostConnKey(sess.Host)
}

func (a *App) connectedKeys() map[string]bool {
	keys := map[string]bool{}
	if key := a.currentHostKey(); key != "" && a.sess != nil {
		keys[key] = true
	}
	a.ensureSessionMaps()
	for key, sess := range a.sessions {
		if sess != nil {
			keys[key] = true
		}
	}
	return keys
}

func (a *App) restoreTerminalForHost(h *vault.Host) bool {
	return a.restoreTerminalForKey(h, hostConnKey(h))
}

func (a *App) restoreTerminalForKey(h *vault.Host, key string) bool {
	if key == "" {
		return false
	}
	a.ensureSessionMaps()
	sess := a.sessionForKey(h, key)
	tm, ok := a.terminals[key]
	if !ok || sess == nil || !tm.available() {
		return false
	}
	a.storeActiveTerminal()
	a.sess = sess
	a.terminal = tm
	a.activeHostKey = key
	a.right = rightTerminal
	a.focus = focusRight
	a.status = fmt.Sprintf("connected to %s", h.Name)
	a.err = ""
	return true
}
