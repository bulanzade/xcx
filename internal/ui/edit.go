package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"xcx/internal/vault"
)

type editKind int

const (
	editKindHost editKind = iota
	editKindGroup
)

// editModel is a form for adding/editing a host or a group.
type editModel struct {
	kind     editKind
	groupIdx int // parent group when adding a host; target group when editing a group
	hostIdx  int // -1 = new

	fields  []*textinput.Model // ordered fields
	cur     int
	heading string
}

func newEditModel(app *App, kind editKind, groupIdx, hostIdx int) editModel {
	m := editModel{kind: kind, groupIdx: groupIdx, hostIdx: hostIdx}
	switch kind {
	case editKindGroup:
		m.heading = "Group"
		name := newField("name", "")
		if groupIdx >= 0 && groupIdx < len(app.vault.Groups) {
			name.SetValue(app.vault.Groups[groupIdx].Name)
		}
		m.fields = []*textinput.Model{&name}
	case editKindHost:
		m.heading = "Host"
		name := newField("name", "")
		addr := newField("address / host", "")
		port := newField("port", "22")
		user := newField("user", "")
		authType := newField("auth (password|key)", "password")
		secret := newField("password", "")
		keyPath := newField("key path", "")
		passphrase := newField("key passphrase", "")
		if groupIdx >= 0 && hostIdx >= 0 && hostIdx < len(app.vault.Groups[groupIdx].Hosts) {
			h := app.vault.Groups[groupIdx].Hosts[hostIdx]
			name.SetValue(h.Name)
			addr.SetValue(h.Addr)
			port.SetValue(strconv.Itoa(portOr22(h.Port)))
			user.SetValue(h.User)
			authType.SetValue(orDefault(h.Auth.Type, "password"))
			secret.SetValue(h.Auth.Password)
			keyPath.SetValue(h.Auth.KeyPath)
			passphrase.SetValue(h.Auth.Passphrase)
		}
		m.fields = []*textinput.Model{&name, &addr, &port, &user, &authType, &secret, &keyPath, &passphrase}
	}
	m.fields[0].Focus()
	return m
}

func newField(placeholder, dflt string) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.CharLimit = 256
	ti.SetValue(dflt)
	return ti
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func (m editModel) Update(app *App, msg tea.Msg) (editModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "tab", "down":
			m.fields[m.cur].Blur()
			m.cur = (m.cur + 1) % len(m.fields)
			m.fields[m.cur].Focus()
		case "shift+tab", "up":
			m.fields[m.cur].Blur()
			m.cur = (m.cur - 1 + len(m.fields)) % len(m.fields)
			m.fields[m.cur].Focus()
		case "enter":
			return m.save(app)
		case "esc":
			app.view = viewHostTree
			return m, nil
		case "ctrl+c", "ctrl+q":
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	*m.fields[m.cur], cmd = m.fields[m.cur].Update(msg)
	return m, cmd
}

func (m editModel) save(app *App) (editModel, tea.Cmd) {
	vals := make([]string, len(m.fields))
	for i, f := range m.fields {
		vals[i] = strings.TrimSpace(f.Value())
	}
	switch m.kind {
	case editKindGroup:
		name := vals[0]
		if name == "" {
			return m, nil
		}
		if m.groupIdx >= 0 {
			app.vault.Groups[m.groupIdx].Name = name
		} else {
			app.vault.Groups = append(app.vault.Groups, vault.Group{Name: name})
		}
	case editKindHost:
		h := vault.Host{
			Name: vals[0], Addr: vals[1], User: vals[3],
			Auth: vault.Auth{Type: vals[4], Password: vals[5], KeyPath: vals[6], Passphrase: vals[7]},
		}
		if p, err := strconv.Atoi(vals[2]); err == nil && p > 0 {
			h.Port = p
		} else {
			h.Port = 22
		}
		if h.Name == "" || h.Addr == "" {
			return m, nil
		}
		if m.groupIdx < 0 || m.groupIdx >= len(app.vault.Groups) {
			// no group selected: create a default one
			app.vault.Groups = append(app.vault.Groups, vault.Group{Name: "default"})
			m.groupIdx = len(app.vault.Groups) - 1
		}
		if m.hostIdx >= 0 {
			app.vault.Groups[m.groupIdx].Hosts[m.hostIdx] = h
		} else {
			app.vault.Groups[m.groupIdx].Hosts = append(app.vault.Groups[m.groupIdx].Hosts, h)
		}
	}
	if err := vault.Save(app.opts.VaultPath, app.master, app.vault); err != nil {
		app.err = fmt.Sprintf("save: %v", err)
	}
	app.hostTree.rebuild(app)
	app.view = viewHostTree
	return m, nil
}

func (m editModel) View(app *App) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(m.heading))
	b.WriteString("\n\n")
	for i, f := range m.fields {
		label := f.Placeholder
		if i == m.cur {
			b.WriteString(cursorStyle.Render("❯ "))
		} else {
			b.WriteString("  ")
		}
		b.WriteString(fmt.Sprintf("%-14s %s\n", label+":", f.View()))
	}
	b.WriteString("\n")
	b.WriteString(dimStyle.Render("[Tab] next  [Enter] save  [Esc] cancel"))
	return b.String()
}
