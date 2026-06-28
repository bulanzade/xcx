package ui

import (
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestUnlock_VaultCreationSetsVerifier is a regression test: the first-run
// vault-creation path must wire up the HostKeyVerifier, otherwise connecting
// to a host fails with "session: a HostKeyVerifier is required".
func TestUnlock_VaultCreationSetsVerifier(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		VaultPath:      filepath.Join(dir, "vault.bin"),
		KnownHostsPath: filepath.Join(dir, "known_hosts"),
	}
	app := New(opts)
	// New() should detect the missing vault and enter creation mode.
	if !app.unlock.creating {
		t.Fatal("expected creation mode for a fresh vault")
	}

	// Stage 0: enter the master password.
	app.unlock, _ = app.unlock.Update(app, typeKeys("masterpw"))
	app.unlock, _ = app.unlock.Update(app, enterKey)
	if app.unlock.stage != 1 {
		t.Fatalf("expected stage 1 after entering password, got %d", app.unlock.stage)
	}
	// Stage 1: confirm.
	app.unlock, _ = app.unlock.Update(app, typeKeys("masterpw"))
	app.unlock, _ = app.unlock.Update(app, enterKey)

	if app.view != viewHostTree {
		t.Fatalf("expected viewHostTree after creation, got %v", app.view)
	}
	if app.verifier == nil {
		t.Fatal("verifier is nil after vault creation — connect will fail")
	}
	if app.vault == nil {
		t.Fatal("vault not attached after creation")
	}
}

// TestUnlock_ExistingVaultSetsVerifier ensures the normal unlock path keeps
// wiring the verifier too.
func TestUnlock_ExistingVaultSetsVerifier(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		VaultPath:      filepath.Join(dir, "vault.bin"),
		KnownHostsPath: filepath.Join(dir, "known_hosts"),
	}
	// First, create a vault the normal way via the creation flow.
	app := New(opts)
	app.unlock, _ = app.unlock.Update(app, typeKeys("masterpw"))
	app.unlock, _ = app.unlock.Update(app, enterKey)
	app.unlock, _ = app.unlock.Update(app, typeKeys("masterpw"))
	app.unlock, _ = app.unlock.Update(app, enterKey)

	// Now simulate a fresh launch against the existing vault: unlock mode.
	app2 := New(opts)
	if app2.unlock.creating {
		t.Fatal("expected unlock mode for an existing vault")
	}
	app2.unlock, _ = app2.unlock.Update(app2, typeKeys("masterpw"))
	app2.unlock, _ = app2.unlock.Update(app2, enterKey)

	if app2.view != viewHostTree {
		t.Fatalf("expected viewHostTree after unlock, got %v", app2.view)
	}
	if app2.verifier == nil {
		t.Fatal("verifier is nil after unlock — connect will fail")
	}
}

// TestUnlock_WrongPasswordStaysOnUnlock verifies a bad password doesn't
// advance and still leaves the verifier unset.
func TestUnlock_WrongPasswordStaysOnUnlock(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		VaultPath:      filepath.Join(dir, "vault.bin"),
		KnownHostsPath: filepath.Join(dir, "known_hosts"),
	}
	// create with "right"
	app := New(opts)
	app.unlock, _ = app.unlock.Update(app, typeKeys("right"))
	app.unlock, _ = app.unlock.Update(app, enterKey)
	app.unlock, _ = app.unlock.Update(app, typeKeys("right"))
	app.unlock, _ = app.unlock.Update(app, enterKey)

	// fresh launch, try the wrong password
	app2 := New(opts)
	app2.unlock, _ = app2.unlock.Update(app2, typeKeys("WRONG"))
	app2.unlock, _ = app2.unlock.Update(app2, enterKey)
	if app2.view != viewUnlock {
		t.Fatalf("expected to stay on viewUnlock after wrong password, got %v", app2.view)
	}
	if app2.verifier != nil {
		t.Fatal("verifier should remain nil on failed unlock")
	}
}

// enterKey is the KeyMsg that unlock.submit reacts to.
var enterKey = tea.Msg(tea.KeyMsg{Type: tea.KeyEnter})

// typeKeys feeds a sequence of runes into the model as one KeyRunes message,
// mimicking typing. Callers should follow up with enterKey to trigger submit.
func typeKeys(s string) tea.Msg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}
