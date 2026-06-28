package sshterm

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// fakePtySession implements PtySession for testing the Terminal loop. It
// replays a scripted stdout and records window-change/input. It mimics the
// real golang.org/x/crypto/ssh.Session ordering rule: StdoutPipe/StderrPipe
// may only be called before Shell() (which sets started). This lets tests
// catch the bug where pipes are requested after the session started.
type fakePtySession struct {
	out                io.Reader // stdout; may be an erroring reader
	stdin              *bytes.Buffer
	stderr             *bytes.Buffer
	mu                 sync.Mutex
	ptyW, ptyH         int
	changedW, changedH int

	closed       bool
	shellStarted bool
	ptyRequested bool
	// stdoutTaken/stderrTaken record whether each pipe was already handed out,
	// to also model "Stdout already set".
	stdoutTaken bool
	stderrTaken bool
}

func newFakePty(out string) *fakePtySession {
	return &fakePtySession{
		out:    bytes.NewBufferString(out),
		stdin:  &bytes.Buffer{},
		stderr: &bytes.Buffer{},
	}
}

// errorReader always errors with err and never returns data.
type errorReader struct{ err error }

func (e errorReader) Read(p []byte) (int, error) { return 0, e.err }

func (f *fakePtySession) RequestPty(term string, h, w int, modes ssh.TerminalModes) error {
	f.ptyRequested = true
	f.ptyW, f.ptyH = w, h
	return nil
}
func (f *fakePtySession) Shell() error { f.shellStarted = true; return nil }
func (f *fakePtySession) WindowChange(h, w int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.changedW, f.changedH = w, h
	return nil
}
func (f *fakePtySession) StdinPipe() (io.WriteCloser, error) { return &bufCloser{f.stdin}, nil }

// errPipeAfterStart mirrors ssh.Session's "StdoutPipe after process started".
var errPipeAfterStart = errors.New("ssh: StdoutPipe after process started")

func (f *fakePtySession) StdoutPipe() (io.Reader, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.shellStarted {
		return nil, errPipeAfterStart
	}
	if f.stdoutTaken {
		return nil, errors.New("ssh: Stdout already set")
	}
	f.stdoutTaken = true
	return f.out, nil
}
func (f *fakePtySession) StderrPipe() (io.Reader, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.shellStarted {
		return nil, errors.New("ssh: StderrPipe after process started")
	}
	if f.stderrTaken {
		return nil, errors.New("ssh: Stderr already set")
	}
	f.stderrTaken = true
	return f.stderr, nil
}
func (f *fakePtySession) Wait() error { return nil }
func (f *fakePtySession) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

type bufCloser struct{ b *bytes.Buffer }

func (c *bufCloser) Write(p []byte) (int, error) { return c.b.Write(p) }
func (c *bufCloser) Close() error                { return nil }

var _ PtySession = (*fakePtySession)(nil)

func TestTerminal_ReplaysOutputToScreen(t *testing.T) {
	fake := newFakePty("hello\r\nworld")
	term, err := NewTerminal(fake, 40, 10)
	if err != nil {
		t.Fatalf("NewTerminal: %v", err)
	}
	if !fake.ptyRequested || !fake.shellStarted {
		t.Fatal("PTY/shell not requested")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	term.Start(ctx)

	select {
	case <-term.Done():
	case <-time.After(time.Second):
		t.Fatal("terminal did not finish reading")
	}
	lines := screenLines(term.Screen())
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "hello" || strings.TrimSpace(lines[1]) != "world" {
		t.Fatalf("screen lines = %v", lines)
	}
}

// TestTerminal_PipesTakenBeforeShell is a regression test for the bug where
// NewTerminal called Shell() first and then StdoutPipe(), causing the real SSH
// library to error "ssh: StdoutPipe after process started" and the shell to
// die instantly ("session ended"). The fake now enforces the same rule, so:
//   - NewTerminal must succeed (pipes taken before Shell), and
//   - the read loop must drain stdout to EOF with Err()==nil (clean exit),
//     not abort with errPipeAfterStart.
func TestTerminal_PipesTakenBeforeShell(t *testing.T) {
	fake := newFakePty("payload")
	term, err := NewTerminal(fake, 80, 24)
	if err != nil {
		t.Fatalf("NewTerminal: %v", err)
	}
	if !fake.stdoutTaken || !fake.stderrTaken {
		t.Fatal("stdout/stderr pipes were not taken before Shell")
	}
	if !fake.shellStarted {
		t.Fatal("Shell was not started")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	term.Start(ctx)

	select {
	case <-term.Done():
	case <-time.After(time.Second):
		t.Fatal("terminal did not finish")
	}
	if err := term.Err(); err != nil {
		t.Fatalf("expected clean EOF (Err==nil), got %v", err)
	}
	if got := strings.TrimSpace(screenLines(term.Screen())[0]); got != "payload" {
		t.Fatalf("screen = %q, want payload", got)
	}
}

// TestTerminal_FailsIfPipesCalledAfterShell documents the contract the fake
// enforces, proving the guard itself works (so the regression test above is
// meaningful and would have failed against the buggy code).
func TestTerminal_FailsIfPipesCalledAfterShell(t *testing.T) {
	fake := newFakePty("x")
	// Simulate the old buggy order: Shell first, then StdoutPipe.
	if err := fake.Shell(); err != nil {
		t.Fatal(err)
	}
	if _, err := fake.StdoutPipe(); err == nil {
		t.Fatal("expected StdoutPipe to fail after Shell, but it succeeded")
	}
}

func TestTerminal_WriteInputAndResize(t *testing.T) {
	fake := newFakePty("") // empty -> EOF quickly
	term, err := NewTerminal(fake, 80, 24)
	if err != nil {
		t.Fatalf("NewTerminal: %v", err)
	}
	if err := term.WriteInput([]byte("ls\n")); err != nil {
		t.Fatalf("WriteInput: %v", err)
	}
	if got := fake.stdin.String(); got != "ls\n" {
		t.Fatalf("stdin = %q, want ls\\n", got)
	}
	if err := term.Resize(120, 40); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if fake.changedW != 120 || fake.changedH != 40 {
		t.Fatalf("window-change = %dx%d, want 120x40", fake.changedW, fake.changedH)
	}
}

func TestTerminal_CloseSetsClosed(t *testing.T) {
	fake := newFakePty("")
	term, _ := NewTerminal(fake, 80, 24)
	if err := term.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !fake.closed {
		t.Fatal("session not closed")
	}
}

func TestTerminal_PropagatesReadError(t *testing.T) {
	fake := &fakePtySession{
		out:    errorReader{errors.New("boom")},
		stdin:  &bytes.Buffer{},
		stderr: &bytes.Buffer{},
	}
	term, _ := NewTerminal(fake, 80, 24)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	term.Start(ctx)
	select {
	case <-term.Done():
	case <-time.After(time.Second):
		t.Fatal("did not finish")
	}
	if err := term.Err(); err == nil || err.Error() != "boom" {
		t.Fatalf("Err = %v, want boom", err)
	}
}
