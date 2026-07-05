package ui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"xcx/internal/transfer"
)

// TestTransferAutoRefreshesStatusWhileActive reproduces the bug where the
// status bar did not update during an SFTP transfer until the user pressed a
// key. The fix arranges a periodic tea.Tick that repaints the status bar while
// a transfer is active, plus a completion message that stops the tick.
//
// This test drives the message loop manually: it marks a transfer active, then
// asserts Update returns a non-nil command on a transferTickMsg (the auto
// repaint continues), and that a transferCompletedMsg returns nil command once
// the transfer finishes (auto repaint stops — no busy loop).
func TestTransferAutoRefreshesStatusWhileActive(t *testing.T) {
	app := New(Options{})
	app.view = viewMain
	app.right = rightSFTP
	app.width, app.height = 120, 40

	// Simulate an in-flight transfer by seeding transfer state directly
	// (mirrors what the consumer goroutine writes under statusMu).
	job := &transfer.Job{Src: "/tmp/payload.bin"}
	app.updateTransferProgress(transfer.Progress{
		Job:       job,
		Done:      256,
		Total:     1024,
		QueueLeft: 0,
	}, time.Now())

	// While the transfer is active, a transferTickMsg must keep the loop alive
	// by scheduling the next tick. nil command would mean the status bar stops
	// refreshing until the next user input — exactly the reported bug.
	_, cmd := app.Update(transferTickMsg{})
	if cmd == nil {
		t.Fatal("Update(transferTickMsg) returned nil cmd while transfer active; status bar won't auto-refresh")
	}

	// The scheduled command must produce another transferTickMsg (the loop is
	// self-sustaining), not some other message.
	msg := executeCmd(t, cmd)
	if _, ok := msg.(transferTickMsg); !ok {
		t.Fatalf("scheduled cmd produced %T, want transferTickMsg", msg)
	}

	// Once the transfer completes, the completion message must stop the auto
	// refresh loop (nil cmd) to avoid a busy loop after transfers end.
	app.finishTransfer(transfer.Completed{Job: &transfer.Job{Src: "/tmp/payload.bin", Status: transfer.StatusDone}})
	_, cmd = app.Update(transferTickMsg{})
	if cmd != nil {
		t.Fatalf("Update(transferTickMsg) after completion returned non-nil cmd; auto-refresh should stop")
	}
}

// TestTransferDoesNotRefreshWhenIdle asserts that with no transfer active the
// status bar does NOT spin an auto-refresh tick (would be a pointless busy
// loop). Update(transferTickMsg) must return nil.
func TestTransferDoesNotRefreshWhenIdle(t *testing.T) {
	app := New(Options{})
	app.view = viewMain
	app.right = rightSFTP
	app.width, app.height = 120, 40

	_, cmd := app.Update(transferTickMsg{})
	if cmd != nil {
		t.Fatal("Update(transferTickMsg) scheduled refresh while idle; should be nil to avoid busy loop")
	}
}

// executeCmd runs a tea.Cmd (which returns a tea.Msg) and returns the message.
func executeCmd(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("cmd is nil")
	}
	msg := cmd()
	if msg == nil {
		t.Fatal("cmd produced nil msg")
	}
	return msg
}
