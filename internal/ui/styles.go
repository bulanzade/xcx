// Package ui implements the Bubble Tea TUI: the top-level model that
// routes between the unlock, host-tree, terminal, and SFTP views.
package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"xcx/internal/sshterm"
)

// isDefaultStyle reports whether st is the "no styling" default (the parser
// represents default fg/bg as -1). We avoid emitting escapes for such cells.
func isDefaultStyle(st sshterm.Style) bool {
	return !st.Bold && !st.Dim && !st.Italic && !st.Under && !st.Rev && st.Fg < 0 && st.Bg < 0
}

// sgrOn returns the CSI sequence that activates st. Returns "" for the default
// style so plain text stays escape-free. Reverse video swaps fg/bg to match
// how terminals render SGR 7m. We emit raw ANSI rather than going through
// lipgloss here so styling is applied regardless of the active color profile
// (lipgloss strips attributes under the Ascii profile used in tests/headless).
func sgrOn(st sshterm.Style) string {
	if isDefaultStyle(st) {
		return ""
	}
	fg, bg := st.Fg, st.Bg
	if st.Rev {
		fg, bg = bg, fg
	}
	var codes []string
	if st.Bold {
		codes = append(codes, "1")
	}
	if st.Dim {
		codes = append(codes, "2")
	}
	if st.Italic {
		codes = append(codes, "3")
	}
	if st.Under {
		codes = append(codes, "4")
	}
	if st.Rev {
		codes = append(codes, "7")
	}
	if fg >= 0 && fg < 8 {
		codes = append(codes, "3"+itoa(int(fg)))
	}
	if bg >= 0 && bg < 8 {
		codes = append(codes, "4"+itoa(int(bg)))
	}
	if len(codes) == 0 {
		return ""
	}
	return "\x1b[" + strings.Join(codes, ";") + "m"
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [4]byte
	n := len(buf)
	for i > 0 {
		n--
		buf[n] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[n:])
}

// renderRow turns one screen row into a styled string by emitting raw SGR
// sequences. Consecutive cells with the same style are grouped into a single
// span (open + text + reset) so we don't emit one escape per character. Blank
// (Ch==0) cells are treated as spaces carrying the cell's style, so background
// or reverse spans stay filled across the whole line width. Default-styled
// cells are emitted as bare text (no escapes) to keep plain output clean.
//
// cursorCol, when >= 0, marks the column of the terminal cursor on this row;
// that cell is rendered with reverse video forced on so the cursor is visible
// as an inverted block regardless of the cell's underlying style.
func renderRow(row []sshterm.Cell, width, cursorCol int) string {
	var b strings.Builder
	var curStyle sshterm.Style
	var styled bool // true while accumulating a styled span
	var seg strings.Builder

	flush := func() {
		if seg.Len() == 0 {
			return
		}
		if styled && sgrOn(curStyle) != "" {
			b.WriteString(sgrOn(curStyle))
			b.WriteString(seg.String())
			b.WriteString("\x1b[0m")
		} else {
			b.WriteString(seg.String())
		}
		seg.Reset()
		styled = false
	}

	for c := 0; c < width; c++ {
		var cell sshterm.Cell
		if c < len(row) {
			cell = row[c]
		}
		ch := cell.Ch
		if ch == 0 {
			ch = ' '
		}
		st := cell.Style
		if c == cursorCol {
			// Render the cursor as a solid reverse-video block. We force the
			// colors to default (terminal fg on bg) and reverse them, so the
			// cursor is visible regardless of the underlying cell's style —
			// including blank cells at end-of-line, whose zero-value style is
			// {Fg:0,Bg:0} (black); without this reset, a reversed black-on-black
			// block was invisible there.
			st = sshterm.Style{Fg: -1, Bg: -1, Rev: true}
		}
		def := isDefaultStyle(st)
		// A boundary occurs when switching between a styled span and plain
		// text, or between two different styles.
		if styled != !def || (!def && st != curStyle) {
			flush()
			curStyle = st
			styled = !def
		}
		seg.WriteRune(ch)
	}
	flush()
	return b.String()
}

// Shared styles applied across all views.
var (
	// appFrame is the outer padding.
	appFrame = lipgloss.NewStyle().Padding(1, 2)

	// titleStyle is used for view headings.
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7AA2F7")).
			MarginBottom(1)

	// subtitleStyle for secondary headings.
	subtitleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#9ECE6A"))

	// statusBarStyle is the persistent bottom bar.
	statusBarStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#BBBBBB")).
			Background(lipgloss.Color("#222222")).
			Padding(0, 1)

	// errorStyle for inline error text.
	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#F7768E"))

	// successStyle for confirmations.
	successStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#9ECE6A"))

	// cursorStyle highlights the selected list row.
	cursorStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7AA2F7"))

	// dimStyle for hints/help.
	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#565F89"))

	// groupStyle for group node labels in the tree.
	groupStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#E0AF68"))

	// paneBorderStyle draws the SFTP dual-pane borders.
	paneBorderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			Padding(0, 1)

	// paneActiveStyle makes the focused pane border stand out.
	paneActiveStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#7AA2F7")).
			Padding(0, 1)

	// dirStyle colors directory entries.
	dirStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7AA2F7")).
			Bold(true)
)
