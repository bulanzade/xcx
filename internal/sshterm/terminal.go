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
	Shell() error
	WindowChange(h, w int) error
	StdinPipe() (io.WriteCloser, error)
	StdoutPipe() (io.Reader, error)
	StderrPipe() (io.Reader, error)
	Wait() error
	Close() error
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
func NewTerminal(sess PtySession, width, height int) (*Terminal, error) {
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
	if err := sess.RequestPty("xterm", height, width, ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}); err != nil {
		return nil, fmt.Errorf("sshterm: request pty: %w", err)
	}
	if err := sess.Shell(); err != nil {
		return nil, fmt.Errorf("sshterm: start shell: %w", err)
	}
	screen := NewScreen(width)
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
	// (or render stray digits like "10;?11;?") if no reply arrives.
	t.parser.SetResponder(func(b []byte) {
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

// NewTerminalWithScreen builds a Terminal that has no live PTY but owns the
// given Screen. It is intended for tests that exercise the Screen-backed
// helpers (Scroll, View, rendering) without standing up a real SSH session.
// The read loop is not started.
func NewTerminalWithScreen(screen *Screen) *Terminal {
	return &Terminal{screen: screen}
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

// Resize updates the PTY size. Safe to call while running.
func (t *Terminal) Resize(width, height int) error {
	if width < 1 || height < 1 {
		return fmt.Errorf("sshterm: invalid size %dx%d", width, height)
	}
	t.mu.Lock()
	t.width, t.height = width, height
	t.mu.Unlock()
	return t.sess.WindowChange(height, width)
}

// WriteInput sends raw bytes (already-encoded key sequences) to the remote
// shell's stdin.
func (t *Terminal) WriteInput(b []byte) error {
	if t.stdin == nil {
		return errors.New("sshterm: stdin not available")
	}
	_, err := t.stdin.Write(b)
	return err
}

// Close shuts down the terminal: cancels stdin and closes the session.
func (t *Terminal) Close() error {
	if t.stdin != nil {
		_ = t.stdin.Close()
	}
	return t.sess.Close()
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
