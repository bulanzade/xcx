package ui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"xcx/internal/session"
	"xcx/internal/transfer"
	"xcx/internal/vault"
)

// view is the active screen of the app.
type view int

const (
	viewUnlock view = iota
	viewHostTree
	viewTerminal
	viewSFTP
	viewHostKeyPrompt // modal-ish: confirm an unknown host key
	viewEdit          // add/edit a host
)

// Options configure how the App is built (paths, callbacks for vault ops).
type Options struct {
	// VaultPath is the encrypted config file path.
	VaultPath string
	// KnownHostsPath is the known_hosts file path.
	KnownHostsPath string
}

// App is the top-level Bubble Tea model. It owns the active view and the
// shared services (vault, session manager, transfer queue) plus the current
// terminal width/height for child views.
type App struct {
	opts Options

	width, height int

	view view

	// services
	vault    *vault.Vault
	master   string // current master password (needed to Save)
	verifier *session.HostKeyVerifier
	queue    *transfer.Queue

	// active connection
	sess *session.Session

	// child view state
	unlock   unlockModel
	hostTree hostTreeModel
	terminal terminalModel
	sftp     sftpModel
	hostKey  hostKeyModel
	edit     editModel

	// transient status line content
	status string
	err    string
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
		return a, nil

	case tea.KeyMsg:
		// Global hotkeys (only when not typing in a text field / terminal):
		if a.view != viewTerminal && a.view != viewUnlock && a.view != viewEdit {
			switch msg.Type {
			case tea.KeyCtrlC:
				return a, tea.Quit
			case tea.KeyCtrlQ:
				return a, tea.Quit
			}
		}
		if a.view == viewTerminal && msg.Type == tea.KeyCtrlBackslash {
			// leave terminal back to host tree
			a.closeTerminal()
			a.view = viewHostTree
			return a, nil
		}
	case dialResultMsg:
		// An async SSH dial completed (terminal or SFTP). Route the result
		// without blocking — the dial already ran in a goroutine via dialCmd.
		return a, a.handleDialResult(msg)
	}

	// Dispatch to the active view.
	var cmd tea.Cmd
	switch a.view {
	case viewUnlock:
		a.unlock, cmd = a.unlock.Update(a, msg)
	case viewHostTree:
		a.hostTree, cmd = a.hostTree.Update(a, msg)
	case viewTerminal:
		a.terminal, cmd = a.terminal.Update(a, msg)
	case viewSFTP:
		a.sftp, cmd = a.sftp.Update(a, msg)
	case viewHostKeyPrompt:
		a.hostKey, cmd = a.hostKey.Update(a, msg)
	case viewEdit:
		a.edit, cmd = a.edit.Update(a, msg)
	}
	return a, cmd
}

// View renders the active screen.
func (a *App) View() string {
	var body string
	switch a.view {
	case viewUnlock:
		body = a.unlock.View(a)
	case viewHostTree:
		body = a.hostTree.View(a)
	case viewTerminal:
		body = a.terminal.View(a)
	case viewSFTP:
		body = a.sftp.View(a)
	case viewHostKeyPrompt:
		body = a.hostKey.View(a)
	case viewEdit:
		body = a.edit.View(a)
	default:
		body = "(unknown view)"
	}

	// The terminal view is full-screen raw; it does not get the frame/status.
	if a.view == viewTerminal {
		return body
	}

	out := appFrame.Render(body)
	if a.err != "" {
		out += "\n" + errorStyle.Render(a.err)
	}
	out += "\n" + a.statusBar()
	return out
}

// statusBar renders the persistent bottom bar.
func (a *App) statusBar() string {
	left := a.status
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
	bar := left + " " + repeatChar(' ', fill) + " " + right
	return statusBarStyle.Render(bar)
}

func (a *App) propagateSize() {
	// child views query width/height from the app directly via accessors.
}

func (a *App) closeTerminal() {
	a.terminal.close()
	if a.sess != nil {
		_ = a.sess.Close()
		a.sess = nil
	}
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

// Width returns the full terminal width.
func (a *App) Width() int { return a.width }

// Height returns the full terminal height.
func (a *App) Height() int { return a.height }

func repeatChar(ch rune, n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(ch)
	}
	return string(b)
}
