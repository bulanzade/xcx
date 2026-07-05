package ui

import (
	"testing"

	"xcx/internal/session"
	"xcx/internal/vault"
)

// TestDialSuccessReturnsToMainFromHostKeyPrompt reproduces the bug where, after
// trusting an unknown host key (pressing 'y'), the dial succeeded but the view
// stayed on the host-key prompt instead of switching to the terminal.
//
// Root cause: handleDialResult's success branch set up the terminal/SFTP pane
// (right/focus) but never reset a.view to viewMain. A normal connect starts
// from viewMain so the omission was invisible, but a trust-confirm connect
// starts from viewHostKeyPrompt and was left there.
func TestDialSuccessReturnsToMainFromHostKeyPrompt(t *testing.T) {
	host := &vault.Host{Name: "h", Addr: "10.0.0.5", User: "root"}
	app := New(Options{})
	app.view = viewHostKeyPrompt // simulate the state during the trust prompt

	_, _ = app.Update(dialResultMsg{host: host, key: "h|root|10.0.0.5|22", sess: &session.Session{Host: host}})

	if app.view != viewMain {
		t.Fatalf("view after dial success = %v, want viewMain (was viewHostKeyPrompt at the prompt)", app.view)
	}
}
