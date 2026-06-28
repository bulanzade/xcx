package ui

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"xcx/internal/sftp"
)

// newLocalPane builds a pane backed by the local fs at dir.
func newLocalPane(t *testing.T, dir string) *pane {
	t.Helper()
	p := &pane{
		backend:  sftp.NewLocalBackend(),
		cwd:      dir,
		selected: map[string]bool{},
	}
	if err := p.refresh(); err != nil {
		t.Fatalf("refresh %s: %v", dir, err)
	}
	return p
}

// TestTransfer_EnqueuesAndCopies is the regression test for the bug where
// pressing F5/F6 copied nothing: transfer() called queue.Run with a closure but
// never Enqueued any Job, so Run found an empty queue and returned immediately.
// This test asserts a job lands in the queue AND the file actually transfers.
func TestTransfer_EnqueuesAndCopies(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	// place a file in src
	srcFile := filepath.Join(srcDir, "a.txt")
	if err := os.WriteFile(srcFile, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := New(Options{})
	m := sftpModel{
		local:  newLocalPane(t, srcDir),
		remote: newLocalPane(t, dstDir),
	}
	m.focused = m.local

	// select the file
	m.local.selected["a.txt"] = true

	m.transfer(app, true)

	if n := app.queue.Len(); n == 0 {
		t.Fatal("queue is empty after transfer — job was not enqueued (the bug)")
	}

	// The transfer runs in a goroutine; wait for the destination file to appear.
	dst := filepath.Join(dstDir, "a.txt")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(dst); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("file was not copied to destination: %v (queue still has %d jobs)", err, app.queue.Len())
	}
	if string(got) != "payload" {
		t.Fatalf("copied content = %q, want payload", string(got))
	}
}

// TestTransfer_NoSelectionNoJob ensures transfer is a no-op when nothing is
// selected and the cursor isn't on a file (e.g. on a directory or empty pane).
func TestTransfer_NoSelectionNoJob(t *testing.T) {
	app := New(Options{})
	m := sftpModel{
		local:  newLocalPane(t, t.TempDir()),
		remote: newLocalPane(t, t.TempDir()),
	}
	m.focused = m.local
	// nothing selected, empty dirs -> no current file
	m.transfer(app, true)
	if n := app.queue.Len(); n != 0 {
		t.Fatalf("expected no jobs, got %d", n)
	}
}

// keep context imported even if future refactor drops it.
var _ = context.Background
