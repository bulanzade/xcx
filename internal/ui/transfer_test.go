package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"xcx/internal/sftp"
	"xcx/internal/transfer"
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

	m.transfer(app)

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

func TestSFTPUpdateF5EnqueuesTransfer(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()
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
	m.local.selected["a.txt"] = true

	m, _ = m.Update(app, tea.KeyMsg{Type: tea.KeyF5})

	if n := app.queue.Len(); n == 0 {
		t.Fatal("F5 did not enqueue any transfer job")
	}
}

func TestSFTPConcurrentF5DoesNotDeadlock(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(srcDir, name), []byte("payload"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	app := New(Options{})
	m := sftpModel{
		local:  newLocalPane(t, srcDir),
		remote: newLocalPane(t, dstDir),
	}
	m.focused = m.local
	m.local.selected["a.txt"] = true
	m.transfer(app)
	m.local.selected["b.txt"] = true
	m.transfer(app)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, errA := os.Stat(filepath.Join(dstDir, "a.txt")); errA == nil {
			if _, errB := os.Stat(filepath.Join(dstDir, "b.txt")); errB == nil {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("concurrent F5 transfers did not complete before timeout")
}

func TestStatusBarShowsTransferProgressAndSpeed(t *testing.T) {
	app := New(Options{})
	app.width = 120
	job := &transfer.Job{Src: "/tmp/payload.bin"}
	app.updateTransferProgress(transfer.Progress{
		Job:       job,
		Done:      512,
		Total:     1024,
		QueueLeft: 2,
	}, time.Now().Add(-time.Second))

	out := app.statusBar()
	for _, want := range []string{"transferring payload.bin", "50%", "B/s", "2 queued"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status bar missing %q: %q", want, out)
		}
	}
}

func TestNewLocalSFTPPaneStartsAtWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(old)
	})

	p, err := newLocalSFTPPane()
	if err != nil {
		t.Fatal(err)
	}
	if p.cwd != dir {
		t.Fatalf("local cwd = %q, want working directory %q", p.cwd, dir)
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
	m.transfer(app)
	if n := app.queue.Len(); n != 0 {
		t.Fatalf("expected no jobs, got %d", n)
	}
}

func TestSFTPEscFromDirectSFTPReturnsPlaceholder(t *testing.T) {
	app := New(Options{})
	app.view = viewMain
	app.right = rightSFTP
	app.focus = focusRight
	app.sess = nil
	m := sftpModel{}

	m, _ = m.Update(app, tea.KeyMsg{Type: tea.KeyEsc})

	if app.right != rightPlaceholder {
		t.Fatalf("right = %v, want rightPlaceholder", app.right)
	}
	if app.focus != focusLeft {
		t.Fatalf("focus = %v, want focusLeft", app.focus)
	}
}

func TestSFTPEscWithNoTerminalDoesNotShowStartingTerminal(t *testing.T) {
	app := New(Options{})
	app.view = viewMain
	app.right = rightSFTP
	app.focus = focusRight
	app.sess = nil
	app.terminal = newTerminalModel()
	m := sftpModel{}

	m, _ = m.Update(app, tea.KeyMsg{Type: tea.KeyEsc})

	if out := app.View(); strings.Contains(out, "starting terminal") {
		t.Fatalf("Esc from direct SFTP rendered terminal placeholder: %q", out)
	}
}

func TestSFTPTabCyclesHostLocalRemote(t *testing.T) {
	app := New(Options{})
	app.view = viewMain
	app.right = rightSFTP
	app.focus = focusLeft
	app.sftp = sftpModel{
		local:  newLocalPane(t, t.TempDir()),
		remote: newLocalPane(t, t.TempDir()),
	}
	app.sftp.focused = app.sftp.local

	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyTab})
	if app.focus != focusRight || app.sftp.focused != app.sftp.local {
		t.Fatalf("first Tab should focus local pane, focus=%v focusedLocal=%v", app.focus, app.sftp.focused == app.sftp.local)
	}

	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyTab})
	if app.focus != focusRight || app.sftp.focused != app.sftp.remote {
		t.Fatalf("second Tab should focus remote pane, focus=%v focusedRemote=%v", app.focus, app.sftp.focused == app.sftp.remote)
	}

	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyTab})
	if app.focus != focusLeft {
		t.Fatalf("third Tab should return to host tree, focus=%v", app.focus)
	}
}

func TestSFTPDoesNotHighlightPaneWhenHostTreeFocused(t *testing.T) {
	app := New(Options{})
	app.width, app.height = 120, 30
	app.view = viewMain
	app.right = rightSFTP
	app.focus = focusLeft
	app.sftp = sftpModel{
		local:  newLocalPane(t, t.TempDir()),
		remote: newLocalPane(t, t.TempDir()),
	}
	app.sftp.focused = app.sftp.local

	out := app.sftp.View(app)
	if strings.Contains(out, "\x1b[38;2;122;162;247m") {
		t.Fatalf("SFTP pane rendered active blue border while host tree focused: %q", out)
	}
}

func TestSFTPPaneScrollsToCursor(t *testing.T) {
	p := &pane{
		cwd:      ".",
		selected: map[string]bool{},
		cur:      19,
	}
	for i := 0; i < 20; i++ {
		p.entries = append(p.entries, sftp.Entry{Name: fmt.Sprintf("file-%02d", i)})
	}
	m := sftpModel{focused: p}

	out := m.renderPane(p, true, 30, 8, "Local")
	if !strings.Contains(out, "file-19") {
		t.Fatalf("rendered pane did not scroll to cursor: %q", out)
	}
	if strings.Contains(out, "file-00") {
		t.Fatalf("rendered pane still starts at top instead of scrolling: %q", out)
	}
}
