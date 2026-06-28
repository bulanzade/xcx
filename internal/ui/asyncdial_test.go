package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"xcx/internal/session"
	"xcx/internal/vault"
)

// TestConnectHost_IsAsync is the regression test for the bug where connectHost
// dialed synchronously inside Update, freezing the TUI on slow/unreachable
// hosts. The fix returns a tea.Cmd that performs the dial; Update must NOT have
// set app.sess or changed the view synchronously (that happens when the
// dialResultMsg arrives).
func TestConnectHost_IsAsync(t *testing.T) {
	app := New(Options{})
	// wire a verifier (needed by dialCmd) and a vault with a host
	app.verifier = &session.HostKeyVerifier{DB: session.NewFileHostKeyDB("")}
	app.vault = &vault.Vault{
		Groups: []vault.Group{{
			Name:  "g",
			Hosts: []vault.Host{{Name: "slow", Addr: "127.0.0.1", Port: 1, User: "u", Auth: vault.Auth{Type: "password", Password: "p"}}},
		}},
	}
	app.hostTree = newHostTreeModel(app)
	// select the host node (index 1, after the group at 0)
	app.hostTree.cur = 1

	host := app.vault.Groups[0].Hosts[0]
	_, cmd := app.hostTree.connectHost(app, &host)

	// connectHost must return a non-nil command (the async dial)...
	if cmd == nil {
		t.Fatal("connectHost returned nil cmd — dial is not async (would block Update)")
	}
	// ...and must NOT have set the session/view synchronously.
	if app.sess != nil {
		t.Fatal("connectHost set app.sess synchronously — the dial ran in Update and blocked")
	}
	if app.view == viewTerminal {
		t.Fatal("connectHost switched to terminal view synchronously")
	}
	// status should reflect "connecting" while the dial is in flight.
	if app.status == "" {
		t.Fatal("connectHost did not set a 'connecting' status")
	}
}

// TestConnectHost_CmdEmitsDialResult verifies the returned command produces a
// dialResultMsg. We point at 127.0.0.1:1 (connection refused instantly) so the
// test is fast and doesn't depend on network timeouts — and still proves the
// dial runs off-thread and reports back via the message.
func TestConnectHost_CmdEmitsDialResult(t *testing.T) {
	app := New(Options{})
	app.verifier = &session.HostKeyVerifier{DB: session.NewFileHostKeyDB("")}
	host := &vault.Host{Name: "x", Addr: "127.0.0.1", Port: 1, User: "u", Auth: vault.Auth{Type: "password", Password: "p"}}
	_, cmd := app.hostTree.connectHost(app, host)
	if cmd == nil {
		t.Fatal("nil cmd")
	}
	msg := cmd()
	res, ok := msg.(dialResultMsg)
	if !ok {
		t.Fatalf("dial cmd emitted %T, want dialResultMsg", msg)
	}
	// 127.0.0.1:1 refuses connection -> an error result (not a session).
	if res.sess != nil {
		t.Fatal("expected dial to 127.0.0.1:1 to fail, but got a session")
	}
}

// TestHandleDialResult_Routing verifies handleDialResult routes an error result
// back to the host tree (the dial already failed off-thread).
func TestHandleDialResult_Routing(t *testing.T) {
	app := New(Options{})
	app.view = viewHostTree
	cmd := app.handleDialResult(dialResultMsg{
		host: &vault.Host{Name: "x"},
		err:  tea.ErrProgramKilled, // any non-nil error
	})
	if cmd != nil {
		t.Fatal("error dial should return nil cmd")
	}
	if app.view != viewHostTree {
		t.Fatalf("error dial should stay on host tree, view=%v", app.view)
	}
	if app.err == "" {
		t.Fatal("error dial should set app.err")
	}
}
