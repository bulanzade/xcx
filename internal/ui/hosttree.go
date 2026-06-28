package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"

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
	host := &app.vault.Groups[n.groupIdx].Hosts[n.hostIdx]
	return m.connectHost(app, host, nodeConnKey(n.groupIdx, n.hostIdx))
}

// connectHost kicks off an async dial for the terminal view. The actual
// session.Connect runs in a tea.Cmd (a goroutine) so a slow/unreachable host
// does not freeze the TUI; the result arrives as dialResultMsg, handled by
// App.Update.
func (m hostTreeModel) connectHost(app *App, host *vault.Host, connKey string) (hostTreeModel, tea.Cmd) {
	if app.restoreTerminalForKey(host, connKey) {
		return m, nil
	}
	app.status = fmt.Sprintf("connecting to %s…", host.Name)
	app.err = ""
	return m, dialCmd(app, host, connKey, false)
}

func (m hostTreeModel) openSFTP(app *App) (hostTreeModel, tea.Cmd) {
	n := m.flat[m.cur]
	if n.kind != nodeHost {
		return m, nil
	}
	host := &app.vault.Groups[n.groupIdx].Hosts[n.hostIdx]
	connKey := nodeConnKey(n.groupIdx, n.hostIdx)
	// reuse existing session for the same host, else dial fresh (async)
	if sess := app.sessionForKey(host, connKey); sess != nil {
		oldSess := app.sess
		app.sess = sess
		sm, err := newSFTPModel(app)
		if err != nil {
			app.sess = oldSess
			app.err = fmt.Sprintf("sftp: %v", err)
			return m, nil
		}
		app.sftp = sm
		app.activeSFTPKey = connKey
		app.right = rightSFTP
		app.focus = focusRight
		return m, nil
	}
	app.status = fmt.Sprintf("connecting to %s…", host.Name)
	app.err = ""
	return m, dialCmd(app, host, connKey, true)
}

// dialResultMsg is emitted when an async session.Connect completes. Exactly one
// of sess / hke / err is meaningful.
type dialResultMsg struct {
	host    *vault.Host
	key     string
	sess    *session.Session
	hke     *session.HostKeyError
	err     error
	forSFTP bool // route to SFTP view instead of terminal on success
}

// dialCmd returns a tea.Cmd that dials host off the UI goroutine and emits the
// result. The host pointer is captured so the caller can be cancelled/edited
// meanwhile without affecting the in-flight dial's copy.
func dialCmd(app *App, host *vault.Host, connKey string, forSFTP bool) tea.Cmd {
	verifier := app.verifier
	return func() tea.Msg {
		sess, err := session.Connect(host, session.DialOptions{Verifier: verifier})
		switch {
		case err == nil:
			return dialResultMsg{host: host, key: connKey, sess: sess, forSFTP: forSFTP}
		default:
			if hke, ok := session.IsHostKeyError(err); ok {
				return dialResultMsg{host: host, key: connKey, hke: hke, forSFTP: forSFTP}
			}
			return dialResultMsg{host: host, key: connKey, err: err, forSFTP: forSFTP}
		}
	}
}

// handleDialResult processes a completed async dial, routing to the terminal or
// SFTP view on success, or the host-key prompt / an error otherwise. Called from
// App.Update so it works regardless of which view initiated the dial.
func (a *App) handleDialResult(msg dialResultMsg) tea.Cmd {
	switch {
	case msg.sess != nil:
		a.err = ""
		if msg.forSFTP {
			a.storeActiveTerminal()
			oldSess := a.sess
			a.sess = msg.sess
			a.activeHostKey = msg.key
			a.ensureSessionMaps()
			a.sessions[a.activeHostKey] = msg.sess
			sm, err := newSFTPModel(a)
			if err != nil {
				a.sess = oldSess
				if oldSess != nil && oldSess.Host != nil {
					a.activeHostKey = a.keyForSession(oldSess)
				}
				_ = msg.sess.Close()
				a.err = fmt.Sprintf("sftp: %v", err)
				return nil
			}
			a.sftp = sm
			a.activeSFTPKey = msg.key
			a.status = fmt.Sprintf("connected to %s", msg.host.Name)
			a.right = rightSFTP
			a.focus = focusRight
			return nil
		}
		return openTerminalCmd(a, msg.sess, msg.key)
	case msg.hke != nil:
		a.hostKey = newHostKeyModel(msg.host, msg.hke, msg.key)
		a.hostKey.openSFTPAfter = msg.forSFTP
		a.view = viewHostKeyPrompt
		a.status = ""
		return nil
	default:
		a.err = fmt.Sprintf("connect: %v", msg.err)
		a.status = ""
		return nil
	}
}

func (m hostTreeModel) delete(app *App) (hostTreeModel, tea.Cmd) {
	n := m.flat[m.cur]
	if n.kind == nodeHost {
		app.closeSessionKey(nodeConnKey(n.groupIdx, n.hostIdx))
		g := &app.vault.Groups[n.groupIdx]
		g.Hosts = append(g.Hosts[:n.hostIdx], g.Hosts[n.hostIdx+1:]...)
	} else {
		for hi := range app.vault.Groups[n.groupIdx].Hosts {
			app.closeSessionKey(nodeConnKey(n.groupIdx, hi))
		}
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
	width, height := app.LeftSize()
	var b strings.Builder
	b.WriteString(titleStyle.Render("Hosts"))
	b.WriteString("\n")
	if len(m.flat) == 0 {
		b.WriteString(dimStyle.Render(fitText("No hosts yet", width)))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render(fitText("N group  n host", width)))
	} else {
		connected := app.connectedKeys()
		start, end := m.visibleRange(height - 6)
		for i := start; i < end; i++ {
			n := m.flat[i]
			if i == m.cur {
				b.WriteString(cursorStyle.Render("❯ "))
			} else {
				b.WriteString("  ")
			}
			m.renderNode(&b, app, n, width-2, connected)
		}
	}
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(fitText("[Enter] connect   [s] SFTP", width)))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(fitText("[e] edit   [n] host   [N] group", width)))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(fitText("[x] delete   [Space] collapse", width)))
	return b.String()
}

func (m hostTreeModel) visibleRange(maxLines int) (int, int) {
	if len(m.flat) == 0 {
		return 0, 0
	}
	if maxLines < 1 {
		maxLines = 1
	}
	start := 0
	if m.cur >= maxLines {
		start = m.cur - maxLines + 1
	}
	end := start + maxLines
	if end > len(m.flat) {
		end = len(m.flat)
		start = end - maxLines
		if start < 0 {
			start = 0
		}
	}
	return start, end
}

func (m hostTreeModel) renderNode(b *strings.Builder, app *App, n treeNode, width int, connected map[string]bool) {
	if n.kind == nodeGroup {
		g := app.vault.Groups[n.groupIdx]
		marker := "▾"
		if n.collapsed {
			marker = "▸"
		}
		b.WriteString(groupStyle.Render(fitText(fmt.Sprintf("%s %s (%d)", marker, g.Name, len(g.Hosts)), width)))
		b.WriteString("\n")
		return
	}
	h := app.vault.Groups[n.groupIdx].Hosts[n.hostIdx]
	prefix := "  "
	if connected[nodeConnKey(n.groupIdx, n.hostIdx)] {
		prefix = connectedStyle.Render("● ")
	}
	b.WriteString(prefix)
	b.WriteString(fitText(fmt.Sprintf("%s@%s:%d", h.User, h.Addr, portOr22(h.Port)), width-2))
	b.WriteString("\n")
}

func portOr22(p int) int {
	if p == 0 {
		return 22
	}
	return p
}

func nodeConnKey(groupIdx, hostIdx int) string {
	return fmt.Sprintf("node|%d|%d", groupIdx, hostIdx)
}

func fitText(s string, width int) string {
	if width < 1 {
		return ""
	}
	if runewidth.StringWidth(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if used+rw > width-1 {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	b.WriteRune('…')
	return b.String()
}
