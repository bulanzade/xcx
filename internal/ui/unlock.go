package ui

import (
	"fmt"
	"os"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"xcx/internal/session"
	"xcx/internal/vault"
)

// unlockModel is the master-password entry screen.
type unlockModel struct {
	input    textinput.Model
	attempts int
	creating bool            // true when no vault exists yet (set master password)
	confirm  textinput.Model // confirm password during creation
	stage    int             // 0=enter, 1=confirm (creation only)
	err      string
}

func newUnlockModel() unlockModel {
	ti := textinput.New()
	ti.Placeholder = "master password"
	ti.EchoMode = textinput.EchoPassword
	ti.Focus()
	ti.CharLimit = 256
	return unlockModel{input: ti, confirm: newConfirmInput()}
}

func newConfirmInput() textinput.Model {
	ti := textinput.New()
	ti.Placeholder = "confirm password"
	ti.EchoMode = textinput.EchoPassword
	ti.CharLimit = 256
	return ti
}

// enterHostTree performs the shared post-unlock setup: it wires up the
// host-key verifier (always required by session.Connect) and builds the host
// tree, then transitions to it. Called from both the vault-creation and the
// vault-unlock paths so neither can forget the verifier.
func (app *App) enterHostTree() {
	if app.verifier == nil {
		app.verifier = &session.HostKeyVerifier{DB: session.NewFileHostKeyDB(app.opts.KnownHostsPath)}
	}
	app.hostTree = newHostTreeModel(app)
	app.view = viewMain
	app.right = rightPlaceholder
	app.focus = focusLeft
}

// detectedNoVault reports whether the configured vault file is absent.
func (m *unlockModel) detectCreating(opts Options) {
	if _, err := os.Stat(opts.VaultPath); os.IsNotExist(err) {
		m.creating = true
	}
}

// Update handles key entry; on Enter it attempts unlock or creation.
func (m unlockModel) Update(app *App, msg tea.Msg) (unlockModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEnter:
			return m.submit(app)
		case tea.KeyCtrlC:
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	if m.stage == 0 {
		m.input, cmd = m.input.Update(msg)
	} else {
		m.confirm, cmd = m.confirm.Update(msg)
	}
	return m, cmd
}

func (m unlockModel) submit(app *App) (unlockModel, tea.Cmd) {
	if m.creating {
		if m.stage == 0 {
			pw := m.input.Value()
			if len(pw) < 4 {
				m.err = "password too short (min 4 chars)"
				return m, nil
			}
			m.stage = 1
			m.confirm.Focus()
			return m, nil
		}
		// confirm
		if m.input.Value() != m.confirm.Value() {
			m.err = "passwords do not match"
			m.stage = 0
			m.confirm.Reset()
			return m, nil
		}
		// create empty vault
		v := &vault.Vault{}
		if err := vault.Save(app.opts.VaultPath, m.input.Value(), v); err != nil {
			m.err = fmt.Sprintf("create vault: %v", err)
			return m, nil
		}
		app.vault = v
		app.master = m.input.Value()
		app.enterHostTree()
		return m, nil
	}

	// unlocking existing vault
	pw := m.input.Value()
	v, err := vault.Open(app.opts.VaultPath, pw)
	if err != nil {
		m.attempts++
		m.err = "wrong password"
		m.input.Reset()
		if m.attempts >= 3 {
			return m, tea.Quit
		}
		return m, nil
	}
	app.vault = v
	app.master = pw
	app.enterHostTree()
	return m, nil
}

// View renders the unlock/creation screen.
func (m unlockModel) View(app *App) string {
	if m.creating {
		title := titleStyle.Render("Set a master password")
		hint := dimStyle.Render("This encrypts your host configuration. There is no recovery if lost.")
		cur := m.input.View()
		if m.stage == 0 {
			return fmt.Sprintf("%s\n%s\n\n%s\n\n%s", title, hint, cur, dimStyle.Render("[Enter] continue"))
		}
		return fmt.Sprintf("%s\n%s\n\n%s\n%s\n\n%s", title, hint, cur, m.confirm.View(), dimStyle.Render("[Enter] confirm"))
	}
	title := titleStyle.Render("Unlock xcx")
	if m.err != "" {
		return fmt.Sprintf("%s\n\n%s\n\n%s", title, m.input.View(), errorStyle.Render(m.err))
	}
	return fmt.Sprintf("%s\n\n%s", title, m.input.View())
}
