package ui

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"xcx/internal/sftp"
	"xcx/internal/transfer"
)

// pane is one side of the dual-pane file manager: a backend + cwd + selection.
type pane struct {
	backend sftp.Backend
	cwd     string
	entries []sftp.Entry
	cur     int
	// selected holds names of multi-selected entries for batch transfer.
	selected map[string]bool
}

func (p *pane) refresh() error {
	entries, err := p.backend.ReadDir(p.cwd)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir // directories first
		}
		return entries[i].Name < entries[j].Name
	})
	p.entries = entries
	if p.cur >= len(entries) {
		p.cur = len(entries) - 1
	}
	if p.cur < 0 {
		p.cur = 0
	}
	return nil
}

func (p *pane) current() *sftp.Entry {
	if p.cur < 0 || p.cur >= len(p.entries) {
		return nil
	}
	return &p.entries[p.cur]
}

// toggleSelect flips the selection of the current entry.
func (p *pane) toggleSelect() {
	e := p.current()
	if e == nil || e.IsDir {
		return
	}
	if p.selected == nil {
		p.selected = map[string]bool{}
	}
	if p.selected[e.Name] {
		delete(p.selected, e.Name)
	} else {
		p.selected[e.Name] = true
	}
}

// sftpModel is the dual-pane SFTP view.
type sftpModel struct {
	local    *pane
	remote   *pane
	focused  *pane               // points to local or remote
	remoteBk *sftp.RemoteBackend // non-nil when remote side is open
}

func newSFTPModel(app *App) (sftpModel, error) {
	localPane, err := newLocalSFTPPane()
	if err != nil {
		return sftpModel{}, err
	}
	rb, err := sftp.NewRemoteBackend(app.sess.Client())
	if err != nil {
		return sftpModel{}, fmt.Errorf("open sftp: %w", err)
	}
	remotePane := &pane{
		backend:  rb,
		cwd:      ".",
		selected: map[string]bool{},
	}
	if err := remotePane.refresh(); err != nil {
		_ = rb.Close()
		return sftpModel{}, fmt.Errorf("list remote: %w", err)
	}
	return sftpModel{local: localPane, remote: remotePane, focused: localPane, remoteBk: rb}, nil
}

func newLocalSFTPPane() (*pane, error) {
	cwd, err := os.Getwd()
	if err != nil || cwd == "" {
		cwd = "."
	}
	localPane := &pane{
		backend:  sftp.NewLocalBackend(),
		cwd:      cwd,
		selected: map[string]bool{},
	}
	if err := localPane.refresh(); err != nil {
		return nil, fmt.Errorf("list cwd: %w", err)
	}
	return localPane, nil
}

// close releases the SFTP subsystem.
func (m *sftpModel) close() {
	if m.remoteBk != nil {
		_ = m.remoteBk.Close()
	}
}

func (m sftpModel) Update(app *App, msg tea.Msg) (sftpModel, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch {
	case k.Type == tea.KeyTab:
		if m.focused == m.local {
			m.focused = m.remote
		} else {
			m.focused = m.local
		}
	case k.String() == "up", k.String() == "k":
		if m.focused.cur > 0 {
			m.focused.cur--
		}
	case k.String() == "down", k.String() == "j":
		if m.focused.cur < len(m.focused.entries)-1 {
			m.focused.cur++
		}
	case k.String() == "enter":
		m.enterDir()
	case k.String() == "backspace", k.String() == "h":
		m.goUp()
	case k.String() == " ":
		m.focused.toggleSelect()
		if m.focused.cur < len(m.focused.entries)-1 {
			m.focused.cur++
		}
	case k.String() == "r":
		_ = m.focused.refresh()
	case k.Type == tea.KeyF5, k.String() == "t":
		// F5 and 't' (transfer) are aliases. Direction is decided by which
		// pane is focused (Local→Remote is upload, Remote→Local is download),
		// not by the key. F6 is intentionally NOT a separate action: Midnight
		// Commander's F5/F6 = copy/move split is not implemented here, so a
		// second identical key would just be a misleading alias.
		m.transfer(app)
		// Kick off the status-bar auto-refresh loop so the running percentage
		// and speed repaint during the transfer without needing user input.
		// App.Update reschedules the tick only while a transfer stays active.
		return m, transferTick()
	case k.Type == tea.KeyF7:
		m.mkdir()
	case k.Type == tea.KeyF8, k.String() == "delete":
		m.remove()
	case k.String() == "esc":
		m.close()
		app.activeSFTPKey = ""
		if app.terminal.term != nil {
			app.right = rightTerminal
			app.focus = focusRight
		} else {
			app.right = rightPlaceholder
			app.focus = focusLeft
		}
	case k.String() == "ctrl+c", k.String() == "ctrl+q":
		return m, tea.Quit
	}
	return m, nil
}

// enterDir descends into the selected directory.
func (m *sftpModel) enterDir() {
	e := m.focused.current()
	if e == nil || !e.IsDir {
		return
	}
	next := joinPath(m.focused.cwd, e.Name)
	m.focused.cwd = next
	if err := m.focused.refresh(); err != nil {
		m.focused.cwd = parentOf(m.focused.cwd)
		_ = m.focused.refresh()
	}
}

func (m *sftpModel) goUp() {
	up := parentOf(m.focused.cwd)
	if up == m.focused.cwd {
		return
	}
	m.focused.cwd = up
	_ = m.focused.refresh()
}

func (m *sftpModel) mkdir() {
	// Minimal: create "newdir"; real UI would prompt. Kept simple for MVP.
	name := "newdir"
	target := joinPath(m.focused.cwd, name)
	_ = m.focused.backend.Mkdir(target)
	_ = m.focused.refresh()
}

func (m *sftpModel) remove() {
	e := m.focused.current()
	if e == nil {
		return
	}
	_ = m.focused.backend.Remove(joinPath(m.focused.cwd, e.Name))
	_ = m.focused.refresh()
}

// transfer enqueues jobs for the selected entries from the focused pane to the
// other pane and runs them via the app's transfer queue.
func (m *sftpModel) transfer(app *App) {
	src := m.focused
	var dst *pane
	if src == m.local {
		dst = m.remote
	} else {
		dst = m.local
	}
	// gather selected files, else the current file if it's a regular file.
	var names []string
	for n := range src.selected {
		names = append(names, n)
	}
	if e := src.current(); e != nil && !e.IsDir && len(names) == 0 {
		names = append(names, e.Name)
	}
	if len(names) == 0 {
		return
	}

	// Capture the backends/paths the Runner needs. Each job carries its own
	// resolved src/dst path; the Runner does the actual copy for whatever
	// (srcPath, dstPath) the queue hands it.
	srcBk, dstBk := src.backend, dst.backend
	jobs := make([]*transfer.Job, 0, len(names))
	for _, n := range names {
		dir := transfer.DirUpload
		if src == m.remote {
			dir = transfer.DirDownload
		}
		jobs = append(jobs, &transfer.Job{
			Src:       joinPath(src.cwd, n),
			Dst:       joinPath(dst.cwd, n),
			Direction: dir,
		})
	}
	app.queue.Enqueue(jobs...)

	run := func(srcPath, dstPath string, prog func(done, total int64)) (int64, error) {
		return sftp.Copy(srcBk, dstBk, srcPath, dstPath, prog)
	}
	progress := make(chan transfer.Progress, 16)
	completed := make(chan transfer.Completed, 16)
	// Run the queue and consume its progress on separate goroutines. Run sends
	// progress synchronously while copying, so the consumer must stay active to
	// keep large transfers from blocking on a full channel.
	go func() {
		started := time.Now()
		for progress != nil || completed != nil {
			select {
			case p, ok := <-progress:
				if !ok {
					progress = nil
					continue
				}
				app.updateTransferProgress(p, started)
			case c, ok := <-completed:
				if !ok {
					completed = nil
					continue
				}
				app.finishTransfer(c)
			}
		}
	}()
	go func() {
		_ = app.queue.Run(context.Background(), run, progress, completed)
		close(progress)
		close(completed)
		_ = src.refresh()
		_ = dst.refresh()
	}()
	src.selected = map[string]bool{}
}

// View renders the two panes side by side.
func (m sftpModel) View(app *App) string {
	w, h := app.RightSize()
	paneW := (w - 3) / 2 // 3 for the gap and borders
	if paneW < 20 {
		paneW = 20
	}
	bodyH := h - 3 // room for header + footer

	rightFocused := app.focus == focusRight
	left := m.renderPane(m.local, rightFocused && m.focused == m.local, paneW, bodyH, "Local")
	right := m.renderPane(m.remote, rightFocused && m.focused == m.remote, paneW, bodyH, "Remote")
	row := lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)

	footer := dimStyle.Render("[Tab] switch  [Enter] open  [Backspace] up  [Space] select  [F5/t] copy  [F7] mkdir  [F8] del  [r] refresh  [Esc] back")
	return titleStyle.Render("SFTP") + "\n" + row + "\n" + footer
}

func (m sftpModel) renderPane(p *pane, active bool, width, height int, label string) string {
	style := paneBorderStyle
	if active {
		style = paneActiveStyle
	}
	innerW := width - 2 // border padding
	if innerW < 4 {
		innerW = 4
	}

	var b strings.Builder
	b.WriteString(subtitleStyle.Render(label))
	b.WriteString("  ")
	b.WriteString(dimStyle.Render(p.cwd))
	b.WriteString("\n")

	rows := height - 2 // header + cwd lines
	start, end := visibleEntryRange(len(p.entries), p.cur, rows)
	for i := start; i < end; i++ {
		e := p.entries[i]
		marker := " "
		if p.selected[e.Name] {
			marker = "●"
		}
		cursor := " "
		if i == p.cur && active {
			cursor = "❯"
		}
		name := e.Name
		if e.IsDir {
			name = dirStyle.Render(name + "/")
		}
		line := fmt.Sprintf("%s%s %s", cursor, marker, name)
		// truncate to inner width
		if lipgloss.Width(line) > innerW {
			line = line[:innerW-1] + "…"
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return style.Width(width).Height(height).Render(strings.TrimRight(b.String(), "\n"))
}

func visibleEntryRange(total, cur, rows int) (int, int) {
	if total <= 0 || rows <= 0 {
		return 0, 0
	}
	if cur < 0 {
		cur = 0
	}
	if cur >= total {
		cur = total - 1
	}
	start := 0
	if cur >= rows {
		start = cur - rows + 1
	}
	end := start + rows
	if end > total {
		end = total
		start = end - rows
		if start < 0 {
			start = 0
		}
	}
	return start, end
}

// joinPath joins dir + name, preserving the path style of dir. The remote pane
// always uses "/"; the local pane on Windows uses "\". We detect the active
// separator from dir so navigation works on both platforms.
func joinPath(dir, name string) string {
	if name == ".." {
		return parentOf(dir)
	}
	if dir == "." {
		return name
	}
	sep := pathSep(dir)
	return strings.TrimRight(dir, sep) + sep + name
}

// pathSep returns the separator used by p: "\\" if p is a backslash path
// (typical Windows local path with no forward slashes), else "/".
func pathSep(p string) string {
	if strings.Contains(p, "\\") && !strings.Contains(p, "/") {
		return "\\"
	}
	return "/"
}

// parentOf returns the parent directory of p ("." has no parent -> "."). It
// handles both "/" and "\" separators so local navigation works on Windows,
// where os.UserHomeDir() returns paths like C:\Users\alice.
func parentOf(p string) string {
	if p == "." || p == "/" || p == "" {
		return p
	}
	// A Windows drive root like "C:\" has no parent — return it as-is so
	// Backspace doesn't jump to "." (the process CWD).
	if len(p) == 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') {
		return p
	}
	sep := pathSep(p)
	trimmed := strings.TrimRight(p, sep)
	idx := strings.LastIndex(trimmed, sep)
	switch {
	case idx < 0:
		return "."
	case idx == 0:
		return sep
	default:
		// Preserve Windows drive roots: "C:\Users" -> "C:\" (not "C:"),
		// otherwise navigating above a drive root would jump to ".".
		parent := p[:idx]
		if len(parent) == 2 && parent[1] == ':' {
			return parent + sep
		}
		return parent
	}
}
