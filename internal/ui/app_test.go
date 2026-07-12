package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"xcx/internal/session"
	"xcx/internal/sshterm"
	"xcx/internal/vault"
)

func TestEncodeKey(t *testing.T) {
	cases := []struct {
		name string
		msg  tea.KeyMsg
		want string
	}{
		{"enter", tea.KeyMsg{Type: tea.KeyEnter}, "\r"},
		{"backspace", tea.KeyMsg{Type: tea.KeyBackspace}, "\x7f"},
		{"tab", tea.KeyMsg{Type: tea.KeyTab}, "\t"},
		{"esc", tea.KeyMsg{Type: tea.KeyEsc}, "\x1b"},
		{"up", tea.KeyMsg{Type: tea.KeyUp}, "\x1b[A"},
		{"down", tea.KeyMsg{Type: tea.KeyDown}, "\x1b[B"},
		{"right", tea.KeyMsg{Type: tea.KeyRight}, "\x1b[C"},
		{"left", tea.KeyMsg{Type: tea.KeyLeft}, "\x1b[D"},
		{"delete", tea.KeyMsg{Type: tea.KeyDelete}, "\x1b[3~"},
		{"ctrl-c", tea.KeyMsg{Type: tea.KeyCtrlC}, "\x03"},
		{"ctrl-z", tea.KeyMsg{Type: tea.KeyCtrlZ}, "\x1a"},
		{"rune a", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}, "a"},
		{"rune hi", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hi")}, "hi"},
		// Windows reports a lone Ctrl press as KeyRunes with a NUL rune; it
		// must NOT be forwarded (shells echo NUL as "^@"). See issue: pressing
		// Ctrl while running `docker logs -f` printed one "^@" per Ctrl press.
		{"rune nul dropped", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{0}}, ""},
		{"rune ctrl+nul mixed", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a', 0, 'b'}}, "ab"},
		{"lone ctrl (KeyNull)", tea.KeyMsg{Type: tea.KeyNull}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := string(encodeKey(c.msg))
			if got != c.want {
				t.Fatalf("encodeKey = %q, want %q", got, c.want)
			}
		})
	}
}

// TestEncodeKey_WindowsLoneCtrlNoNUL is a focused regression test for the bug
// where pressing Ctrl (alone) in the terminal view on Windows wrote a NUL
// byte to the remote PTY for every press, echoed by the shell as "^@".
//
// On Windows, bubbletea's console reader reports a lone Ctrl (and some Ctrl+
// combos with no dedicated control code) as KeyMsg{Type: KeyRunes, Runes: [0]},
// because the console event's Char is 0 when Ctrl yields no printable char.
// Before the fix, encodeKey forwarded that NUL straight to the PTY.
func TestEncodeKey_WindowsLoneCtrlNoNUL(t *testing.T) {
	// Several lone-Ctrl presses, each as a KeyRunes with a NUL rune.
	for i := 0; i < 16; i++ {
		got := encodeKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{0}})
		if len(got) != 0 {
			t.Fatalf("press %d: encodeKey produced %q, want no bytes (NUL must not reach PTY)", i, got)
		}
	}
	// The Unix equivalent (KeyNull / KeyCtrlAt from a real 0x00 byte) must
	// also not inject a NUL for a bare Ctrl press.
	if got := encodeKey(tea.KeyMsg{Type: tea.KeyNull}); len(got) != 0 {
		t.Fatalf("KeyNull encoded to %q, want no bytes", got)
	}
}

func TestParentOf(t *testing.T) {
	cases := map[string]string{
		"/a/b/c": "/a/b",
		"/a":     "/",
		"/":      "/",
		".":      ".",
		"a/b":    "a",
		"a":      ".",
		"/a/b/":  "/a",
		// Windows-style backslash paths (local pane from os.UserHomeDir):
		`C:\Users\alice`:      `C:\Users`,
		`C:\Users`:            `C:\`,
		`C:\`:                 `C:\`,
		`C:\Users\alice\docs`: `C:\Users\alice`,
	}
	for in, want := range cases {
		if got := parentOf(in); got != want {
			t.Errorf("parentOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJoinPath(t *testing.T) {
	if got := joinPath(".", "x"); got != "x" {
		t.Fatalf("joinPath(.,x) = %q, want x", got)
	}
	if got := joinPath("/a/b", "x"); got != "/a/b/x" {
		t.Fatalf("joinPath(/a/b,x) = %q, want /a/b/x", got)
	}
	if got := joinPath("/a/b/", "x"); got != "/a/b/x" {
		t.Fatalf("joinPath(/a/b/,x) = %q, want /a/b/x", got)
	}
	if got := joinPath("/a/b", ".."); got != "/a" {
		t.Fatalf("joinPath(/a/b,..) = %q, want /a", got)
	}
	// Windows-style backslash: separator preserved.
	if got := joinPath(`C:\Users\alice`, "docs"); got != `C:\Users\alice\docs` {
		t.Fatalf("joinPath backslash = %q, want C:\\Users\\alice\\docs", got)
	}
	if got := joinPath(`C:\Users\alice`, ".."); got != `C:\Users` {
		t.Fatalf("joinPath backslash ..= %q, want C:\\Users", got)
	}
}

// TestHostTreeBuildsFromVault verifies the tree is built from the vault and
// that host nodes resolve to the right host.
func TestHostTreeBuildsFromVault(t *testing.T) {
	app := New(Options{})
	app.vault = &vault.Vault{
		Groups: []vault.Group{
			{Name: "g1", Hosts: []vault.Host{{Name: "h1", Addr: "1.1.1.1", User: "u", Auth: vault.Auth{Type: "password", Password: "p"}}}},
		},
	}
	app.hostTree = newHostTreeModel(app)
	// expect 2 nodes: group + 1 host
	if len(app.hostTree.flat) != 2 {
		t.Fatalf("flat nodes = %d, want 2", len(app.hostTree.flat))
	}
	if app.hostTree.flat[0].kind != nodeGroup || app.hostTree.flat[1].kind != nodeHost {
		t.Fatal("node kinds wrong")
	}

	// collapsing the group should hide its hosts
	app.hostTree.flat[0].collapsed = true
	app.hostTree.rebuild(app)
	if len(app.hostTree.flat) != 1 {
		t.Fatalf("after collapse, nodes = %d, want 1", len(app.hostTree.flat))
	}
}

func TestPortOr22(t *testing.T) {
	if portOr22(0) != 22 || portOr22(2222) != 2222 {
		t.Fatal("portOr22 wrong")
	}
}

func TestFitTextTruncatesWithoutWrapping(t *testing.T) {
	got := fitText("root@124.223.9.88:22", 12)
	if got != "root@124.22…" {
		t.Fatalf("fitText = %q, want root@124.22…", got)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("fitText wrapped unexpectedly: %q", got)
	}
}

func TestHostTreeSelectedRowsKeepDetails(t *testing.T) {
	app := New(Options{})
	app.width, app.height = 120, 30
	app.vault = &vault.Vault{
		Groups: []vault.Group{{
			Name: "tencent",
			Hosts: []vault.Host{{
				Name: "124.223.9.88",
				Addr: "124.223.9.88",
				User: "root",
				Port: 22,
				Auth: vault.Auth{Type: "key"},
			}},
		}},
	}
	app.hostTree = newHostTreeModel(app)

	app.hostTree.cur = 0
	if out := app.hostTree.View(app); !strings.Contains(out, "tencent") {
		t.Fatalf("selected group name missing: %q", out)
	}

	app.hostTree.cur = 1
	out := app.hostTree.View(app)
	if !strings.Contains(out, "root@124.223.9.88:22") {
		t.Fatalf("selected host details missing: %q", out)
	}
}

func TestHostTreeMarksConnectedHost(t *testing.T) {
	host := vault.Host{Name: "prod", Addr: "10.0.0.1", User: "root", Port: 22}
	app := New(Options{})
	app.width, app.height = 120, 30
	app.vault = &vault.Vault{
		Groups: []vault.Group{{
			Name: "g",
			Hosts: []vault.Host{
				host,
				{Name: "other", Addr: "10.0.0.2", User: "root", Port: 22},
			},
		}},
	}
	app.ensureSessionMaps()
	app.sessions[nodeConnKey(0, 0)] = &session.Session{Host: &host}
	app.hostTree = newHostTreeModel(app)

	out := app.hostTree.View(app)
	if !strings.Contains(out, "● root@10.0.0.1:22") {
		t.Fatalf("connected marker missing: %q", out)
	}
	if strings.Contains(out, "● root@10.0.0.2:22") {
		t.Fatalf("disconnected host was marked connected: %q", out)
	}
}

func TestDeleteConnectedHostClosesCachedSession(t *testing.T) {
	host := vault.Host{Name: "prod", Addr: "10.0.0.1", User: "root", Port: 22}
	app := New(Options{})
	app.vault = &vault.Vault{
		Groups: []vault.Group{{
			Name:  "g",
			Hosts: []vault.Host{host},
		}},
	}
	app.hostTree = newHostTreeModel(app)
	app.hostTree.cur = 1
	app.ensureSessionMaps()
	app.sessions[nodeConnKey(0, 0)] = &session.Session{Host: &host}

	app.hostTree, _ = app.hostTree.delete(app)

	if app.sessions[nodeConnKey(0, 0)] != nil {
		t.Fatal("deleted host left cached session behind")
	}
}

func TestHostTreeHelpWrapsKeysInBrackets(t *testing.T) {
	app := New(Options{})
	app.width, app.height = 120, 30
	app.vault = &vault.Vault{Groups: []vault.Group{{Name: "g"}}}
	app.hostTree = newHostTreeModel(app)

	out := app.hostTree.View(app)
	for _, want := range []string{"[Enter] connect", "[s] SFTP", "[e] edit", "[n] host", "[N] group", "[x] delete", "[Space] collapse"} {
		if !strings.Contains(out, want) {
			t.Fatalf("host tree help missing %q: %q", want, out)
		}
	}
}

func TestTerminalTabPassesThroughWhenFocused(t *testing.T) {
	app := New(Options{})
	app.view = viewMain
	app.right = rightTerminal
	app.focus = focusRight
	var wrote []byte
	app.terminal = terminalModel{
		writeInput: func(input []byte) error {
			wrote = append(wrote, input...)
			return nil
		},
	}

	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyTab})

	if app.focus != focusRight {
		t.Fatalf("focus = %v, want focusRight", app.focus)
	}
	if string(wrote) != "\t" {
		t.Fatalf("terminal got %q, want tab", string(wrote))
	}
}

func TestTerminalShiftTabReturnsToHostTree(t *testing.T) {
	app := New(Options{})
	app.view = viewMain
	app.right = rightTerminal
	app.focus = focusRight

	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyShiftTab})

	if app.focus != focusLeft {
		t.Fatalf("focus = %v, want focusLeft", app.focus)
	}
}

func TestRightSizeSubtractsTerminalBorder(t *testing.T) {
	app := New(Options{})
	app.width, app.height = 100, 40
	app.view = viewMain
	app.right = rightTerminal

	w, h := app.RightSize()
	if w != 61 || h != 37 {
		t.Fatalf("RightSize terminal = %dx%d, want 61x37", w, h)
	}
}

func TestRemoteDirForKeyUsesBackgroundTerminal(t *testing.T) {
	app := New(Options{})
	screen := sshterm.NewScreen(80)
	screen.SetCurrentDir("~")
	sshterm.NewParser(screen).Write([]byte("root@server:/srv/background# "))
	term := sshterm.NewTerminalWithScreen(screen)
	host := &vault.Host{User: "root"}
	app.terminals = map[string]terminalModel{
		"host-key": {term: term},
	}
	app.sessions = map[string]*session.Session{
		"host-key": {Host: host},
	}

	if got, want := app.remoteDirForKey("host-key"), "/srv/background"; got != want {
		t.Fatalf("remoteDirForKey() = %q, want %q", got, want)
	}
}

func TestEditHostAuthTypeIsChoice(t *testing.T) {
	app := New(Options{})
	app.vault = &vault.Vault{Groups: []vault.Group{{Name: "g"}}}
	m := newEditModel(app, editKindHost, 0, -1)
	m.cur = authFieldIndex
	m.fields[m.cur].Focus()

	m, _ = m.Update(app, tea.KeyMsg{Type: tea.KeySpace})
	if got := m.fields[authFieldIndex].Value(); got != "key" {
		t.Fatalf("auth after space = %q, want key", got)
	}

	m, _ = m.Update(app, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("password")})
	if got := m.fields[authFieldIndex].Value(); got != "key" {
		t.Fatalf("auth accepted typed text, got %q", got)
	}

	m, _ = m.Update(app, tea.KeyMsg{Type: tea.KeyRight})
	if got := m.fields[authFieldIndex].Value(); got != "password" {
		t.Fatalf("auth after right = %q, want password", got)
	}
}

func TestAddrOf(t *testing.T) {
	h := &vault.Host{Addr: "10.0.0.1", Port: 2222}
	if got := addrOf(h); got != "10.0.0.1:2222" {
		t.Fatalf("addrOf = %q, want 10.0.0.1:2222", got)
	}
	h2 := &vault.Host{Addr: "h"}
	if got := addrOf(h2); got != "h:22" {
		t.Fatalf("addrOf default port = %q, want h:22", got)
	}
}
