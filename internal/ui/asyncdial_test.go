package ui

import (
	"errors"
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
	_, cmd := app.hostTree.connectHost(app, &host, nodeConnKey(0, 0))

	// connectHost must return a non-nil command (the async dial)...
	if cmd == nil {
		t.Fatal("connectHost returned nil cmd — dial is not async (would block Update)")
	}
	// ...and must NOT have set the session/view synchronously.
	if app.sess != nil {
		t.Fatal("connectHost set app.sess synchronously — the dial ran in Update and blocked")
	}
	if app.right == rightTerminal {
		t.Fatal("connectHost switched to terminal pane synchronously")
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
	_, cmd := app.hostTree.connectHost(app, host, nodeConnKey(0, 0))
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
	app.view = viewMain
	cmd := app.handleDialResult(dialResultMsg{
		host: &vault.Host{Name: "x"},
		err:  tea.ErrProgramKilled, // any non-nil error
	})
	if cmd != nil {
		t.Fatal("error dial should return nil cmd")
	}
	if app.view != viewMain {
		t.Fatalf("error dial should stay on main split view, view=%v", app.view)
	}
	if app.err == "" {
		t.Fatal("error dial should set app.err")
	}
}

func TestHandleDialResultTerminalKeepsOldSessionUntilStarted(t *testing.T) {
	oldHost := &vault.Host{Name: "old", Addr: "10.0.0.1", User: "root"}
	newHost := &vault.Host{Name: "new", Addr: "10.0.0.2", User: "root"}
	oldSess := &session.Session{Host: oldHost}
	newSess := &session.Session{Host: newHost}
	app := New(Options{})
	app.view = viewMain
	app.right = rightTerminal
	app.focus = focusRight
	app.sess = oldSess

	cmd := app.handleDialResult(dialResultMsg{
		host: newHost,
		sess: newSess,
	})

	if cmd == nil {
		t.Fatal("successful terminal dial should return open terminal command")
	}
	if app.sess != oldSess {
		t.Fatal("old session was replaced before new terminal started")
	}
	if app.right != rightTerminal {
		t.Fatalf("right = %v, want existing terminal pane", app.right)
	}
}

func TestTerminalStartedReplacesSessionOnlyAfterPTYReady(t *testing.T) {
	oldHost := &vault.Host{Name: "old", Addr: "10.0.0.1", User: "root"}
	newHost := &vault.Host{Name: "new", Addr: "10.0.0.2", User: "root"}
	oldSess := &session.Session{Host: oldHost}
	newSess := &session.Session{Host: newHost}
	app := New(Options{})
	app.view = viewMain
	app.right = rightTerminal
	app.focus = focusLeft
	app.sess = oldSess

	app.terminal, _ = app.terminal.Update(app, terminalStartedMsg{sess: newSess})

	if app.sess != newSess {
		t.Fatal("new session was not installed after terminal started")
	}
	if app.focus != focusRight {
		t.Fatalf("focus = %v, want focusRight", app.focus)
	}
	if app.sessionForHost(oldHost) != oldSess {
		t.Fatal("old session was not kept in the background cache")
	}
}

func TestAppRoutesTerminalStartedWhilePlaceholderActive(t *testing.T) {
	newHost := &vault.Host{Name: "new", Addr: "10.0.0.2", User: "root"}
	newSess := &session.Session{Host: newHost}
	app := New(Options{})
	app.view = viewMain
	app.right = rightPlaceholder
	app.focus = focusLeft

	_, _ = app.Update(terminalStartedMsg{sess: newSess})

	if app.sess != newSess {
		t.Fatal("terminalStartedMsg was not routed to terminal model")
	}
	if app.right != rightTerminal {
		t.Fatalf("right = %v, want rightTerminal", app.right)
	}
	if app.status != "connected to new" {
		t.Fatalf("status = %q, want connected to new", app.status)
	}
}

func TestTerminalErrorForNewSessionKeepsOldSession(t *testing.T) {
	oldHost := &vault.Host{Name: "old", Addr: "10.0.0.1", User: "root"}
	newHost := &vault.Host{Name: "new", Addr: "10.0.0.2", User: "root"}
	oldSess := &session.Session{Host: oldHost}
	newSess := &session.Session{Host: newHost}
	app := New(Options{})
	app.view = viewMain
	app.right = rightTerminal
	app.focus = focusRight
	app.sess = oldSess

	app.terminal, _ = app.terminal.Update(app, terminalErrorMsg{sess: newSess, err: errors.New("pty failed")})

	if app.sess != oldSess {
		t.Fatal("old session was replaced/closed after new terminal error")
	}
	if app.right != rightTerminal {
		t.Fatalf("right = %v, want existing terminal pane", app.right)
	}
	if app.err == "" {
		t.Fatal("terminal error was not reported")
	}
}

func TestRestoreBackgroundTerminalForPreviousHost(t *testing.T) {
	oldHost := &vault.Host{Name: "old", Addr: "10.0.0.1", User: "root"}
	newHost := &vault.Host{Name: "new", Addr: "10.0.0.2", User: "root"}
	oldSess := &session.Session{Host: oldHost}
	newSess := &session.Session{Host: newHost}
	app := New(Options{})
	app.view = viewMain
	app.right = rightTerminal
	app.focus = focusRight
	app.sess = oldSess
	app.activeHostKey = hostConnKey(oldHost)
	oldWrote := false
	app.terminal = terminalModel{
		writeInput: func([]byte) error {
			oldWrote = true
			return nil
		},
	}

	app.terminal, _ = app.terminal.Update(app, terminalStartedMsg{sess: newSess})

	if !app.restoreTerminalForHost(oldHost) {
		t.Fatal("failed to restore old host terminal")
	}
	if app.sess != oldSess {
		t.Fatal("old session was not restored")
	}
	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if !oldWrote {
		t.Fatal("old terminal was not restored as active writer")
	}
}

func TestCloseTerminalKeepsSessionWhenSFTPUsesSameHost(t *testing.T) {
	host := &vault.Host{Name: "h", Addr: "10.0.0.1", User: "root"}
	sess := &session.Session{Host: host}
	app := New(Options{})
	app.view = viewMain
	app.sess = sess
	app.activeHostKey = hostConnKey(host)
	app.activeSFTPKey = hostConnKey(host)
	app.ensureSessionMaps()
	app.sessions[app.activeHostKey] = sess
	app.terminal = terminalModel{
		writeInput: func([]byte) error { return nil },
	}

	app.closeTerminal()

	if app.sessions[hostConnKey(host)] != sess {
		t.Fatal("closeTerminal removed session still owned by SFTP")
	}
}

func TestShutdownClearsCachedSessionsAndTerminals(t *testing.T) {
	host := &vault.Host{Name: "h", Addr: "10.0.0.1", User: "root"}
	app := New(Options{})
	app.sess = &session.Session{Host: host}
	app.activeHostKey = hostConnKey(host)
	app.ensureSessionMaps()
	app.sessions[app.activeHostKey] = app.sess
	app.terminals[app.activeHostKey] = terminalModel{writeInput: func([]byte) error { return nil }}

	app.shutdown()

	if app.sess != nil || len(app.sessions) != 0 || len(app.terminals) != 0 {
		t.Fatalf("shutdown left state: sess=%v sessions=%d terminals=%d", app.sess, len(app.sessions), len(app.terminals))
	}
}
