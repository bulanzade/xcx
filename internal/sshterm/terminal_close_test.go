package sshterm

import (
	"io"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// blockingClosePty fakes a session whose stdin writes block until Close: the
// stdin pipe has no reader, so a reply wedges there exactly like a query
// response stuck on an SSH channel that stopped accepting data. Close tears
// both pipe directions down, which is what must unblock the writer.
type blockingClosePty struct {
	outR *io.PipeReader
	outW *io.PipeWriter // feeds the parser (session stdout)
	inR  *io.PipeReader
	inW  *io.PipeWriter // session stdin; writes block until inR is read/closed
}

func newBlockingClosePty() *blockingClosePty {
	outR, outW := io.Pipe()
	inR, inW := io.Pipe()
	return &blockingClosePty{outR: outR, outW: outW, inR: inR, inW: inW}
}

func (b *blockingClosePty) RequestPty(string, int, int, ssh.TerminalModes) error { return nil }
func (b *blockingClosePty) Setenv(string, string) error                          { return nil }
func (b *blockingClosePty) Shell() error                                         { return nil }
func (b *blockingClosePty) WindowChange(int, int) error                          { return nil }
func (b *blockingClosePty) StdinPipe() (io.WriteCloser, error)                   { return b.inW, nil }
func (b *blockingClosePty) StdoutPipe() (io.Reader, error)                       { return b.outR, nil }
func (b *blockingClosePty) StderrPipe() (io.Reader, error) {
	return strings.NewReader(""), nil
}
func (b *blockingClosePty) Wait() error { return nil }
func (b *blockingClosePty) Close() error {
	b.inR.Close()
	b.outW.Close()
	return nil
}

// TestCloseUnblocksBlockedWriter pins the disconnect deadlock: a query reply
// (DSR) whose stdin write is wedged holds writeMu. Close used to wait for
// that lock BEFORE closing the session, so it could never acquire it and
// disconnect hung forever. Closing the session first fails the blocked
// write, releasing the lock; Close must then complete promptly.
func TestCloseUnblocksBlockedWriter(t *testing.T) {
	fake := newBlockingClosePty()
	term, err := NewTerminal(fake, 80, 24)
	if err != nil {
		t.Fatalf("NewTerminal: %v", err)
	}

	// The DSR query makes the parser reply synchronously inside Write; the
	// reply write to the reader-less stdin pipe blocks forever, holding
	// writeMu.
	wrote := make(chan struct{})
	go func() {
		defer close(wrote)
		term.parser.Write([]byte("\x1b[6n"))
	}()
	// Let the reply write wedge on the pipe before closing.
	time.Sleep(100 * time.Millisecond)
	select {
	case <-wrote:
		t.Fatal("setup: the DSR reply did not block on stdin; deadlock scenario not exercised")
	default:
	}

	done := make(chan error, 1)
	go func() { done <- term.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close hung on writeMu held by a blocked reply writer; the session must be closed before waiting for the lock")
	}
	// The wedged writer was released by the session teardown.
	select {
	case <-wrote:
	case <-time.After(time.Second):
		t.Fatal("blocked reply writer was not released by Close")
	}
}
