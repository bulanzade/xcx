package sshterm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"golang.org/x/crypto/ssh"
)

// PtySession is the subset of *ssh.Session the Terminal needs. Defined as an
// interface so tests can supply a fake (and so the Terminal doesn't depend on
// the concrete ssh.Session for its core loop).
type PtySession interface {
	RequestPty(term string, h, w int, modes ssh.TerminalModes) error
	// Setenv requests a remote environment variable before Shell. Servers
	// reject pairs outside sshd's AcceptEnv; callers treat it as best-effort.
	Setenv(name, value string) error
	Shell() error
	WindowChange(h, w int) error
	StdinPipe() (io.WriteCloser, error)
	StdoutPipe() (io.Reader, error)
	StderrPipe() (io.Reader, error)
	Wait() error
	Close() error
}

// TerminalOption customizes terminal creation.
type TerminalOption func(*terminalOptions)

type terminalOptions struct {
	env map[string]string
}

// WithEnv asks the remote shell to export name=value (best-effort; sshd's
// AcceptEnv usually restricts what is accepted, failures are ignored).
func WithEnv(name, value string) TerminalOption {
	return func(o *terminalOptions) {
		if o.env == nil {
			o.env = map[string]string{}
		}
		o.env[name] = value
	}
}

// Terminal drives an interactive remote shell: it requests a PTY, pumps
// remote output through a Parser into a Screen, sends local input to the
// remote, and lets the view resize the PTY. It is safe to Read the Screen
// from the UI goroutine while the read loop runs in the background.
type Terminal struct {
	sess   PtySession
	screen *Screen
	parser *Parser

	stdin io.WriteCloser
	// writeMu serializes writes to stdin: input from the UI goroutine
	// (keys, mouse events) races the read loop's query replies (DSR/DA/OSC
	// responses), and the underlying ssh channel writer is not safe for
	// concurrent use — interleaved writes corrupt each other's packets.
	writeMu sync.Mutex
	// out/err are obtained before Shell() starts the session: the SSH library
	// forbids calling StdoutPipe/StderrPipe once Shell has run (it sets
	// started=true). We stash them here so the read loop can use them.
	out  io.Reader
	errr io.Reader

	mu     sync.Mutex
	width  int
	height int

	// closed when the remote side exits or Run is cancelled.
	done chan struct{}
	// err holds the terminal error after it stops (nil = clean exit).
	err error
}

// NewTerminal creates a Terminal over sess. Call Start to begin.
//
// The pipes MUST be requested before Shell() (which flips the session's
// started flag); doing it afterward makes StdoutPipe return
// "ssh: StdoutPipe after process started" and the shell dies instantly.
func NewTerminal(sess PtySession, width, height int, opts ...TerminalOption) (*Terminal, error) {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	in, err := sess.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("sshterm: stdin pipe: %w", err)
	}
	out, err := sess.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("sshterm: stdout pipe: %w", err)
	}
	errR, err := sess.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("sshterm: stderr pipe: %w", err)
	}
	// TERM=xterm-256color: with plain "xterm" (8 colors), remote programs that
	// probe terminfo (htop, codex, pi/lipgloss) downgrade their palette to the
	// basic 8/16 colors or none at all. The 256-color terminfo entry ships with
	// every modern ncurses installation.
	if err := sess.RequestPty("xterm-256color", height, width, ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}); err != nil {
		return nil, fmt.Errorf("sshterm: request pty: %w", err)
	}
	var cfg terminalOptions
	for _, opt := range opts {
		opt(&cfg)
	}
	for name, value := range cfg.env {
		_ = sess.Setenv(name, value) // best-effort: gated by sshd AcceptEnv
	}
	if err := sess.Shell(); err != nil {
		return nil, fmt.Errorf("sshterm: start shell: %w", err)
	}
	screen := NewScreen(width)
	// Seed the screen with the PTY height so an alternate-screen switch
	// (ESC [ ? 1049 h) before the first render sizes the alt viewport
	// correctly, instead of clamping it to 1 row (NewScreen doesn't know the
	// height; SetHeight also records it as the main buffer's scroll clamp).
	screen.SetHeight(height)
	t := &Terminal{
		sess:   sess,
		screen: screen,
		parser: NewParser(screen),
		stdin:  in,
		out:    out,
		errr:   errR,
		width:  width,
		height: height,
		done:   make(chan struct{}),
	}
	// Wire query responses (DSR cursor report, device attributes) back to the
	// remote process's stdin. vim/less/tmux send these on startup and misbehave
	// (or render stray digits like "10;11;?") if no reply arrives. The write
	// takes writeMu: replies race UI-side input writes (keys, mouse events)
	// on the same stream.
	t.parser.SetResponder(func(b []byte) {
		// stdin is read under the lock: Close nils it while holding writeMu,
		// so checking outside the critical section would race it.
		t.writeMu.Lock()
		defer t.writeMu.Unlock()
		if t.stdin != nil {
			_, _ = t.stdin.Write(b)
		}
	})
	return t, nil
}

// Start launches the background read loop. It returns immediately. The loop
// stops when ctx is cancelled or the remote shell exits.
func (t *Terminal) Start(ctx context.Context) {
	go t.readLoop(ctx)
}

// Screen returns the screen model for rendering. It is safe to read
// concurrently with the running read loop.
func (t *Terminal) Screen() *Screen { return t.screen }

// FeedOutput parses b as if it arrived from the remote PTY, updating the
// screen and mode-tracking state (DECCKM, bracketed paste, etc.). It is the
// synchronous equivalent of the readLoop's parser.Write, intended for tests
// that need to drive the terminal into a specific mode without a live session.
func (t *Terminal) FeedOutput(b []byte) {
	if t.parser != nil {
		t.parser.Write(b)
	}
}

// CurrentDir returns the remote shell working directory most recently
// reported through terminal integration. It is empty until the shell emits a
// supported OSC sequence or title.
func (t *Terminal) CurrentDir() string {
	if t == nil || t.screen == nil {
		return ""
	}
	return t.screen.CurrentDir()
}

// PromptDir returns a working directory inferred from a conventional shell
// prompt currently under the cursor.
func (t *Terminal) PromptDir(user string) string {
	if t == nil || t.screen == nil {
		return ""
	}
	return t.screen.PromptDir(user)
}

// BracketedPaste reports whether pasted input should be wrapped in xterm's
// bracketed paste markers for the remote program.
func (t *Terminal) BracketedPaste() bool {
	return t.parser != nil && t.parser.BracketedPaste()
}

// ApplicationCursor reports whether the remote program enabled DECCKM
// application cursor keys (e.g. vim/less). When true, arrow keys must be sent
// in the application encoding (ESC O A/B/C/D), and Shift+arrow keys belong to
// the remote program rather than local scroll-back.
func (t *Terminal) ApplicationCursor() bool {
	return t.parser != nil && t.parser.ApplicationCursor()
}

// MouseConfig reports the xterm mouse reporting modes the remote program
// enabled (htop setup dialogs, vim, tmux). While Reporting is true, mouse
// events over the terminal pane must be forwarded to the remote program
// instead of driving local selection/scrollback.
func (t *Terminal) MouseConfig() MouseConfig {
	if t.parser == nil {
		return MouseConfig{}
	}
	return t.parser.MouseConfig()
}

// NewTerminalWithScreen builds a Terminal that has no live PTY but owns the
// given Screen. It is intended for tests that exercise the Screen-backed
// helpers (Scroll, View, rendering) without standing up a real SSH session.
// The read loop is not started. A parser is attached so mode-tracking helpers
// (BracketedPaste, ApplicationCursor) work when tests feed escape sequences
// directly via the parser.
func NewTerminalWithScreen(screen *Screen) *Terminal {
	return &Terminal{screen: screen, parser: NewParser(screen)}
}

// Scroll moves the terminal view by delta rows within scrollback (positive =
// up into history, negative = back toward live output). The offset is clamped
// by the screen. The scroll is automatically cancelled when new remote output
// arrives, so the view returns to live output.
func (t *Terminal) Scroll(delta int) { t.screen.Scroll(delta) }

// Width/Height report the current PTY dimensions.
func (t *Terminal) Width() int  { t.mu.Lock(); defer t.mu.Unlock(); return t.width }
func (t *Terminal) Height() int { t.mu.Lock(); defer t.mu.Unlock(); return t.height }

// Done returns a channel closed when the terminal stops.
func (t *Terminal) Done() <-chan struct{} { return t.done }

// Err returns the error that stopped the terminal (nil on clean exit). Only
// meaningful after Done is closed.
func (t *Terminal) Err() error { t.mu.Lock(); defer t.mu.Unlock(); return t.err }

// Resize updates the PTY size. Safe to call while running; the whole update
// (stored size, screen buffers, PTY window) is serialized so two concurrent
// calls can't interleave into a mixed state.
func (t *Terminal) Resize(width, height int) error {
	if width < 1 || height < 1 {
		return fmt.Errorf("sshterm: invalid size %dx%d", width, height)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.width, t.height = width, height
	// Keep the screen's buffers in sync with the PTY size: the alt buffer is a
	// fixed-height viewport, the main buffer uses the height as its scroll
	// clamp ceiling, and both use the width as their wrap width. A width
	// change without the grid re-slice left remote programs drawing at the new
	// width against a stale grid, wrapping full-width TUI boxes mid-screen.
	if t.screen != nil {
		t.screen.SetHeight(height)
		t.screen.SetWidth(width)
	}
	return t.sess.WindowChange(height, width)
}

// WriteInput sends raw bytes (already-encoded key sequences) to the remote
// shell's stdin. Serialized against the read loop's query replies; the ssh
// channel writer is not safe for concurrent callers.
func (t *Terminal) WriteInput(b []byte) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if t.stdin == nil {
		return errors.New("sshterm: stdin not available")
	}
	_, err := t.stdin.Write(b)
	return err
}

// Close shuts down the terminal: closes the session, then cancels stdin.
// The session is torn down FIRST deliberately: a query reply (DSR/DA) stuck
// writing to a wedged SSH channel holds writeMu indefinitely, and taking the
// lock before closing the session made Close hang forever on disconnect.
// Closing the session fails the blocked channel write, which releases the
// lock so the stdin cleanup below can always proceed.
func (t *Terminal) Close() error {
	err := t.sess.Close()
	t.writeMu.Lock()
	if t.stdin != nil {
		_ = t.stdin.Close()
		t.stdin = nil
	}
	t.writeMu.Unlock()
	return err
}

func (t *Terminal) readLoop(ctx context.Context) {
	defer close(t.done)
	// out and errr were captured before Shell() in NewTerminal. The SSH
	// library disallows StdoutPipe/StderrPipe after the session has started.
	out := t.out
	_ = t.errr // stderr is merged conceptually; stdout is the primary stream

	buf := make([]byte, 4096)
	for {
		select {
		case <-ctx.Done():
			t.setErr(ctx.Err())
			return
		default:
		}
		n, err := out.Read(buf)
		if n > 0 {
			// Parse under the screen's row slice mutations as sequential: the
			// read loop is the sole writer, the UI only reads, so no extra lock
			// is needed for correctness of the pointers; transient torn reads
			// of a row are acceptable since the UI re-renders at its own frame
			// rate. New output also cancels any scroll-back so the view jumps
			// back to live (bottom) output, mirroring real terminals.
			t.screen.MarkOutput()
			t.screen.ResetScroll()
			t.parser.Write(buf[:n])
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				t.setErr(nil)
				return
			}
			t.setErr(err)
			return
		}
	}
}

func (t *Terminal) setErr(err error) {
	t.mu.Lock()
	t.err = err
	t.mu.Unlock()
}
