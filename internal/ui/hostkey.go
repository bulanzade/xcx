package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"xcx/internal/session"
	"xcx/internal/vault"
)

// hostKeyModel asks the user to confirm an unknown (or mismatched) host key.
type hostKeyModel struct {
	host *vault.Host
	err  *session.HostKeyError
	// openSFTPAfter, when true, routes a successful connect to the SFTP view
	// instead of the terminal view (set by the caller that initiated SFTP).
	openSFTPAfter bool
}

func newHostKeyModel(host *vault.Host, err *session.HostKeyError) hostKeyModel {
	return hostKeyModel{host: host, err: err}
}

func (m hostKeyModel) Update(app *App, msg tea.Msg) (hostKeyModel, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch strings.ToLower(k.String()) {
	case "y":
		// Only unknown hosts are confirmable; mismatches must never be trusted.
		if !m.err.Unknown {
			app.err = "host key mismatch — refusing to connect"
			app.view = viewHostTree
			return m, nil
		}
		return m.connectTrusted(app)
	case "n", "esc":
		app.view = viewHostTree
		app.status = "connection cancelled"
		return m, nil
	case "ctrl+c", "ctrl+q":
		return m, tea.Quit
	}
	return m, nil
}

// connectTrusted re-dials asynchronously with a verifier that records the
// offered key (TrustOnUnknown), then routes to the terminal/SFTP view via the
// dialResultMsg handler. Running it in a goroutine keeps the TUI responsive
// during the second handshake.
func (m hostKeyModel) connectTrusted(app *App) (hostKeyModel, tea.Cmd) {
	trusted := app.verifier.TrustOnUnknown()
	app.status = fmt.Sprintf("connecting to %s…", m.host.Name)
	app.err = ""
	host := m.host
	return m, func() tea.Msg {
		sess, err := session.Connect(host, session.DialOptions{Verifier: trusted})
		switch {
		case err == nil:
			return dialResultMsg{host: host, sess: sess, forSFTP: m.openSFTPAfter}
		default:
			if hke, ok := session.IsHostKeyError(err); ok {
				return dialResultMsg{host: host, hke: hke, forSFTP: m.openSFTPAfter}
			}
			return dialResultMsg{host: host, err: err, forSFTP: m.openSFTPAfter}
		}
	}
}

// View renders the confirmation.
func (m hostKeyModel) View(app *App) string {
	var b strings.Builder
	if m.err.Unknown {
		b.WriteString(titleStyle.Render("Unknown host"))
	} else {
		b.WriteString(errorStyle.Render("⚠ Host key mismatch"))
	}
	b.WriteString("\n\n")
	b.WriteString(fmt.Sprintf("Host: %s (%s)\n", m.host.Name, addrOf(m.host)))
	b.WriteString(fmt.Sprintf("Key:  %s\n", m.err.Fingerprint))
	b.WriteString("\n")
	if m.err.Unknown {
		b.WriteString(dimStyle.Render("This host is not in your known_hosts.\n\n"))
		b.WriteString(successStyle.Render("Trust and connect? [y/N]"))
	} else {
		b.WriteString(errorStyle.Render("The key does NOT match the recorded one.\n"))
		b.WriteString(errorStyle.Render("This could be a man-in-the-middle attack. Connection refused.\n"))
	}
	return b.String()
}

// addrOf builds the "addr:port" the verifier sees.
func addrOf(h *vault.Host) string {
	p := h.Port
	if p == 0 {
		p = 22
	}
	return fmt.Sprintf("%s:%d", h.Addr, p)
}
