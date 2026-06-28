package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"xcx/internal/session"
	"xcx/internal/vault"
)

// hostTreeModel renders the vault's groups as a flat, navigable list of
// nodes (groups are collapsible headers; hosts are selectable leaves).
type hostTreeModel struct {
	// flat is the list of visible nodes in display order.
	flat []treeNode
	cur  int
}

type nodeKind int

const (
	nodeGroup nodeKind = iota
	nodeHost
)

type treeNode struct {
	kind      nodeKind
	groupIdx  int  // index into vault.Groups
	hostIdx   int  // index into vault.Groups[groupIdx].Hosts (-1 for group)
	collapsed bool // group only
}

func newHostTreeModel(app *App) hostTreeModel {
	m := hostTreeModel{}
	m.rebuild(app)
	return m
}

// rebuild recomputes the flat visible node list from the vault.
func (m *hostTreeModel) rebuild(app *App) {
	// preserve collapse state across rebuilds by group name
	prev := map[string]bool{}
	for _, n := range m.flat {
		if n.kind == nodeGroup && n.groupIdx >= 0 && n.groupIdx < len(app.vault.Groups) {
			prev[app.vault.Groups[n.groupIdx].Name] = n.collapsed
		}
	}
	m.flat = m.flat[:0]
	if app.vault == nil {
		return
	}
	for gi, g := range app.vault.Groups {
		collapsed := prev[g.Name]
		m.flat = append(m.flat, treeNode{kind: nodeGroup, groupIdx: gi, hostIdx: -1, collapsed: collapsed})
		if collapsed {
			continue
		}
		for hi := range g.Hosts {
			m.flat = append(m.flat, treeNode{kind: nodeHost, groupIdx: gi, hostIdx: hi})
		}
	}
	if m.cur >= len(m.flat) {
		m.cur = len(m.flat) - 1
	}
	if m.cur < 0 {
		m.cur = 0
	}
}

func (m hostTreeModel) Update(app *App, msg tea.Msg) (hostTreeModel, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if len(m.flat) == 0 {
		// only allow adding a group
		switch k.String() {
		case "n":
			app.edit = newEditModel(app, editKindGroup, -1, -1)
			app.view = viewEdit
		case "ctrl+c", "ctrl+q":
			return m, tea.Quit
		}
		return m, nil
	}
	switch k.String() {
	case "up", "k":
		if m.cur > 0 {
			m.cur--
		}
	case "down", "j":
		if m.cur < len(m.flat)-1 {
			m.cur++
		}
	case "enter":
		return m.activate(app)
	case " ":
		if m.cur >= 0 && m.flat[m.cur].kind == nodeGroup {
			m.flat[m.cur].collapsed = !m.flat[m.cur].collapsed
			m.rebuild(app)
		}
	case "s":
		return m.openSFTP(app)
	case "e":
		n := m.flat[m.cur]
		if n.kind == nodeHost {
			app.edit = newEditModel(app, editKindHost, n.groupIdx, n.hostIdx)
			app.view = viewEdit
		}
	case "n":
		// 'n' on a group adds host under it; at top adds a group.
		n := m.flat[m.cur]
		if n.kind == nodeGroup {
			app.edit = newEditModel(app, editKindHost, n.groupIdx, -1)
		} else {
			app.edit = newEditModel(app, editKindHost, n.groupIdx, -1)
		}
		app.view = viewEdit
	case "N":
		app.edit = newEditModel(app, editKindGroup, -1, -1)
		app.view = viewEdit
	case "x":
		return m.delete(app)
	case "ctrl+c", "ctrl+q":
		return m, tea.Quit
	}
	return m, nil
}

// activate connects to the selected host (or toggles a group).
func (m hostTreeModel) activate(app *App) (hostTreeModel, tea.Cmd) {
	n := m.flat[m.cur]
	if n.kind == nodeGroup {
		m.flat[m.cur].collapsed = !m.flat[m.cur].collapsed
		m.rebuild(app)
		return m, nil
	}
	host := app.vault.Groups[n.groupIdx].Hosts[n.hostIdx]
	return m.connectHost(app, &host)
}

// connectHost kicks off an async dial for the terminal view. The actual
// session.Connect runs in a tea.Cmd (a goroutine) so a slow/unreachable host
// does not freeze the TUI; the result arrives as dialResultMsg, handled by
// App.Update.
func (m hostTreeModel) connectHost(app *App, host *vault.Host) (hostTreeModel, tea.Cmd) {
	app.status = fmt.Sprintf("connecting to %s…", host.Name)
	app.err = ""
	return m, dialCmd(app, host, false)
}

func (m hostTreeModel) openSFTP(app *App) (hostTreeModel, tea.Cmd) {
	n := m.flat[m.cur]
	if n.kind != nodeHost {
		return m, nil
	}
	host := app.vault.Groups[n.groupIdx].Hosts[n.hostIdx]
	// reuse existing session for the same host, else dial fresh (async)
	if app.sess != nil && app.sess.Host.Name == host.Name {
		sm, err := newSFTPModel(app)
		if err != nil {
			app.err = fmt.Sprintf("sftp: %v", err)
			return m, nil
		}
		app.sftp = sm
		app.view = viewSFTP
		return m, nil
	}
	app.status = fmt.Sprintf("connecting to %s…", host.Name)
	app.err = ""
	return m, dialCmd(app, &host, true)
}

// dialResultMsg is emitted when an async session.Connect completes. Exactly one
// of sess / hke / err is meaningful.
type dialResultMsg struct {
	host    *vault.Host
	sess    *session.Session
	hke     *session.HostKeyError
	err     error
	forSFTP bool // route to SFTP view instead of terminal on success
}

// dialCmd returns a tea.Cmd that dials host off the UI goroutine and emits the
// result. The host pointer is captured so the caller can be cancelled/edited
// meanwhile without affecting the in-flight dial's copy.
func dialCmd(app *App, host *vault.Host, forSFTP bool) tea.Cmd {
	verifier := app.verifier
	return func() tea.Msg {
		sess, err := session.Connect(host, session.DialOptions{Verifier: verifier})
		switch {
		case err == nil:
			return dialResultMsg{host: host, sess: sess, forSFTP: forSFTP}
		default:
			if hke, ok := session.IsHostKeyError(err); ok {
				return dialResultMsg{host: host, hke: hke, forSFTP: forSFTP}
			}
			return dialResultMsg{host: host, err: err, forSFTP: forSFTP}
		}
	}
}

// handleDialResult processes a completed async dial, routing to the terminal or
// SFTP view on success, or the host-key prompt / an error otherwise. Called from
// App.Update so it works regardless of which view initiated the dial.
func (a *App) handleDialResult(msg dialResultMsg) tea.Cmd {
	switch {
	case msg.sess != nil:
		a.sess = msg.sess
		a.status = fmt.Sprintf("connected to %s", msg.host.Name)
		a.err = ""
		if msg.forSFTP {
			sm, err := newSFTPModel(a)
			if err != nil {
				a.err = fmt.Sprintf("sftp: %v", err)
				a.view = viewHostTree
				return nil
			}
			a.sftp = sm
			a.view = viewSFTP
			return nil
		}
		a.terminal = newTerminalModel()
		a.view = viewTerminal
		return openTerminalCmd(a)
	case msg.hke != nil:
		a.hostKey = newHostKeyModel(msg.host, msg.hke)
		a.hostKey.openSFTPAfter = msg.forSFTP
		a.view = viewHostKeyPrompt
		a.status = ""
		return nil
	default:
		a.err = fmt.Sprintf("connect: %v", msg.err)
		a.status = ""
		a.view = viewHostTree
		return nil
	}
}

func (m hostTreeModel) delete(app *App) (hostTreeModel, tea.Cmd) {
	n := m.flat[m.cur]
	if n.kind == nodeHost {
		g := &app.vault.Groups[n.groupIdx]
		g.Hosts = append(g.Hosts[:n.hostIdx], g.Hosts[n.hostIdx+1:]...)
	} else {
		app.vault.Groups = append(app.vault.Groups[:n.groupIdx], app.vault.Groups[n.groupIdx+1:]...)
	}
	if err := vault.Save(app.opts.VaultPath, app.master, app.vault); err != nil {
		app.err = fmt.Sprintf("save: %v", err)
	}
	m.rebuild(app)
	return m, nil
}

// View renders the tree plus a help line.
func (m hostTreeModel) View(app *App) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Hosts"))
	b.WriteString("\n\n")
	if len(m.flat) == 0 {
		b.WriteString(dimStyle.Render("No hosts yet. Press 'N' to add a group, 'n' to add a host."))
		b.WriteString("\n")
	} else {
		for i, n := range m.flat {
			line := m.renderNode(app, n)
			if i == m.cur {
				line = cursorStyle.Render("❯ ") + line
			} else {
				line = "  " + line
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	help := dimStyle.Render("[Enter] connect/toggle  [s] SFTP  [e] edit  [n] host  [N] group  [x] delete  [space] collapse")
	b.WriteString("\n")
	b.WriteString(help)
	return b.String()
}

func (m hostTreeModel) renderNode(app *App, n treeNode) string {
	if n.kind == nodeGroup {
		g := app.vault.Groups[n.groupIdx]
		marker := "▾"
		if n.collapsed {
			marker = "▸"
		}
		return groupStyle.Render(fmt.Sprintf("%s %s (%d)", marker, g.Name, len(g.Hosts)))
	}
	h := app.vault.Groups[n.groupIdx].Hosts[n.hostIdx]
	auth := h.Auth.Type
	if auth == "" {
		auth = "?"
	}
	return fmt.Sprintf("%-16s %s@%s:%d  [%s]", h.Name, h.User, h.Addr, portOr22(h.Port), auth)
}

func portOr22(p int) int {
	if p == 0 {
		return 22
	}
	return p
}
