// Package ui implements the Bubble Tea TUI: the top-level model that
// routes between the unlock, host-tree, terminal, and SFTP views.
package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"xcx/internal/sshterm"
)

// isDefaultStyle reports whether st is the "no styling" default (the parser
// represents default fg/bg as ColorDefault). We avoid emitting escapes for such
// cells.
func isDefaultStyle(st sshterm.Style) bool {
	return !st.Bold && !st.Dim && !st.Italic && !st.Under && !st.Rev && st.Fg == sshterm.ColorDefault && st.Bg == sshterm.ColorDefault
}

// sgrOn returns the CSI sequence that activates st. Returns "" for the default
// style so plain text stays escape-free. Style colors are re-emitted as the
// original SGR parameters together with attribute codes; reverse video is
// emitted as SGR 7 and the HOST terminal performs the fg/bg swap — swapping
// here as well double-inverted Rev+color cells (ESC[7;31m showed red text
// where xterm shows a red background). We emit raw ANSI rather than going
// through lipgloss here so styling is applied regardless of the active color
// profile (lipgloss strips attributes under the Ascii profile used in
// tests/headless).
func sgrOn(st sshterm.Style) string {
	if isDefaultStyle(st) {
		return ""
	}
	fg, bg := st.Fg, st.Bg
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
	if fg != sshterm.ColorDefault {
		codes = append(codes, colorCode(fg, 30, 90, 38))
	}
	if bg != sshterm.ColorDefault {
		codes = append(codes, colorCode(bg, 40, 100, 48))
	}
	if len(codes) == 0 {
		return ""
	}
	return "\x1b[" + strings.Join(codes, ";") + "m"
}

// colorCode renders one Color as SGR parameter text: basic colors as 3x/4x,
// bright as 9x/10x, palette indexes as ext;5;n, and direct colors as
// ext;2;r;g;b (basic/bright/ext are the fg and bg variants of each form).
func colorCode(c sshterm.Color, basic, bright, ext int) string {
	switch {
	case c < 8:
		return itoa(basic + int(c))
	case c < 16:
		return itoa(bright + int(c) - 8)
	case !c.IsRGB():
		return itoa(ext) + ";5;" + itoa(c.PaletteIndex())
	default:
		r, g, b := c.RGB()
		return itoa(ext) + ";2;" + itoa(r) + ";" + itoa(g) + ";" + itoa(b)
	}
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

// cellWidth returns the display width of one remote-grid cell's rune in the
// REMOTE program's coordinate system. East Asian Ambiguous characters
// (box-drawing ─│╭, •, ±, …) count as a single column — matching how the
// remote terminal and TUIs (pi, codex, vim) lay out their output. Plain
// runewidth.RuneWidth flips ambiguous characters to two columns when the
// LOCAL locale is zh/ja/ko, which halved pi's full-width input-box rules and
// clipped right-aligned footer text on Chinese-locale hosts. Wide runes (CJK,
// emoji) still take two columns.
func cellWidth(r rune) int {
	w := runewidth.RuneWidth(r)
	if w == 2 && runewidth.IsAmbiguousWidth(r) {
		return 1
	}
	if w < 1 {
		return 1
	}
	return w
}

// renderRow turns one screen row into a styled string by emitting raw SGR
// sequences. Consecutive cells with the same style are grouped into a single
// span (open + text + reset) so we don't emit one escape per character.
// Blank (Ch==0) cells are unwritten grid fillers; they render as plain spaces
// with the terminal default style (never black-on-black padding), like a real
// terminal's untouched cells. Default-styled cells are emitted as bare text
// (no escapes) to keep plain output clean.
//
// cursorCol, when >= 0, marks the column of the terminal cursor on this row;
// that cell is rendered with reverse video forced on so the cursor is visible
// as an inverted block regardless of the cell's underlying style.
func renderRow(row []sshterm.Cell, width, cursorCol int, selection ...int) string {
	selStart, selEnd := -1, -1
	if len(selection) >= 2 {
		selStart, selEnd = selection[0], selection[1]
		if selStart > selEnd {
			selStart, selEnd = selEnd, selStart
		}
	}
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

	used := 0
	for c := 0; c < width && used < width; c++ {
		var cell sshterm.Cell
		if c < len(row) {
			cell = row[c]
		}
		ch := cell.Ch
		if ch == 0 {
			ch = ' '
		}
		chWidth := cellWidth(ch)
		if used+chWidth > width {
			ch = ' '
			chWidth = width - used
		}
		st := cell.Style
		if cell.Ch == 0 {
			// Filler cells beyond written content are grid zero values
			// (Fg:0/Bg:0 = black); they must render as plain blanks with the
			// terminal default style, like a real terminal's unwritten cells —
			// not black-on-black padding.
			st = sshterm.Style{Fg: sshterm.ColorDefault, Bg: sshterm.ColorDefault}
		}
		if c == cursorCol {
			// Render the cursor as a solid reverse-video block. We force the
			// colors to default (terminal fg on bg) and reverse them, so the
			// cursor is visible regardless of the underlying cell's style —
			// including blank cells at end-of-line, whose zero-value style is
			// {Fg:0,Bg:0} (black); without this reset, a reversed black-on-black
			// block was invisible there.
			st = sshterm.Style{Fg: -1, Bg: -1, Rev: true}
		} else if c >= selStart && c <= selEnd {
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
		used += chWidth
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

	connectedStyle = lipgloss.NewStyle().
			Bold(true).
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

	// leftPaneStyle frames the persistent host tree.
	leftPaneStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#565F89"))

	leftPaneActiveStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#7AA2F7"))

	rightPaneStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#565F89"))

	rightPaneActiveStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#7AA2F7"))

	// dirStyle colors directory entries.
	dirStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7AA2F7")).
			Bold(true)
)
