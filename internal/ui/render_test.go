package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"xcx/internal/sshterm"
)

// hasSGR reports whether s contains an SGR escape sequence whose parameter
// list includes code. SGR codes may be combined (e.g. "\x1b[1;31m"), so we
// scan for any CSI ... m whose params, split on ';', contain code.
func hasSGR(s, code string) bool {
	for i := 0; i < len(s); i++ {
		if i+2 <= len(s) && s[i] == '\x1b' && s[i+1] == '[' {
			// find the terminating 'm'
			j := i + 2
			for j < len(s) && s[j] != 'm' && s[j] != '\x1b' {
				j++
			}
			if j < len(s) && s[j] == 'm' {
				params := strings.Split(s[i+2:j], ";")
				for _, p := range params {
					if p == code {
						return true
					}
				}
			}
		}
	}
	return false
}

// defStyle is the parser's default style (Fg/Bg = -1 = terminal default).
// Cell literals in tests must use this rather than the Go zero value, which
// is Fg=0/Bg=0 = black (a real color, not "default").
func defStyled(ch rune) sshterm.Cell {
	return sshterm.Cell{Ch: ch, Style: sshterm.Style{Fg: -1, Bg: -1}}
}

// TestRenderRow_PlainHasNoEscapes verifies an unstyled (default-style) row is
// rendered as bare text with no escape sequences, so styled rendering doesn't
// pollute plain output.
func TestRenderRow_PlainHasNoEscapes(t *testing.T) {
	row := []sshterm.Cell{
		defStyled('h'), defStyled('i'), defStyled(0), defStyled('!'),
	}
	got := renderRow(row, 4, -1)
	if got != "hi !" {
		t.Fatalf("plain row = %q, want \"hi !\"", got)
	}
	if strings.Contains(got, "\x1b") {
		t.Fatalf("plain row should have no escapes, got %q", got)
	}
}

// TestRenderRow_ReverseHeader reproduces `top`'s reverse-video column header
// row. The header cells carry Style.Rev with default colors (Fg/Bg = -1);
// rendering must emit a reverse-video SGR (CSI 7m) so the header shows a
// filled background. The bug was that the renderer ignored Style entirely and
// printed plain text.
func TestRenderRow_ReverseHeader(t *testing.T) {
	headerText := "  PID USER      PR  NI"
	row := make([]sshterm.Cell, len(headerText))
	for i, ch := range headerText {
		row[i] = sshterm.Cell{Ch: ch, Style: sshterm.Style{Rev: true, Fg: -1, Bg: -1}}
	}
	got := renderRow(row, len(headerText), -1)
	// The text must be present...
	if !strings.Contains(got, "PID USER") {
		t.Fatalf("header text missing: %q", got)
	}
	// ...and it must carry a reverse-video SGR (SGR 7m).
	if !hasSGR(got, "7") {
		t.Fatalf("reverse-video SGR (CSI 7m) missing from header render: %q", got)
	}
}

// TestRenderRow_BoldAndColor verifies bold + foreground color are emitted.
func TestRenderRow_BoldAndColor(t *testing.T) {
	row := []sshterm.Cell{
		{Ch: 'X', Style: sshterm.Style{Bold: true, Fg: 1, Bg: -1}}, // bold red on default bg
	}
	got := renderRow(row, 1, -1)
	if !hasSGR(got, "1") {
		t.Fatalf("bold SGR missing: %q", got)
	}
	if !hasSGR(got, "31") { // fg red = CSI 31m
		t.Fatalf("red foreground SGR missing: %q", got)
	}
}

// TestRenderRow_BackgroundColor verifies a background color (e.g. top's header
// via 47m white bg) is emitted.
func TestRenderRow_BackgroundColor(t *testing.T) {
	row := []sshterm.Cell{
		{Ch: 'H', Style: sshterm.Style{Bg: 7, Fg: -1}}, // white background, default fg
	}
	got := renderRow(row, 1, -1)
	if !hasSGR(got, "47") {
		t.Fatalf("white background SGR (CSI 47m) missing: %q", got)
	}
}

// TestRenderRow_GroupsSameStyle verifies consecutive cells with the same style
// are wrapped in a single SGR span (one open + one reset), not per-character.
func TestRenderRow_GroupsSameStyle(t *testing.T) {
	row := []sshterm.Cell{
		{Ch: 'A', Style: sshterm.Style{Bold: true, Fg: -1, Bg: -1}},
		{Ch: 'B', Style: sshterm.Style{Bold: true, Fg: -1, Bg: -1}},
		{Ch: 'C', Style: sshterm.Style{Bold: true, Fg: -1, Bg: -1}},
	}
	got := renderRow(row, 3, -1)
	// The bold-on sequence (CSI 1m) should appear exactly once.
	if c := strings.Count(got, "\x1b[1m"); c != 1 {
		t.Fatalf("expected one bold-on span, got %d in %q", c, got)
	}
	if !strings.Contains(got, "ABC") {
		t.Fatalf("text broken up: %q", got)
	}
}

// TestRenderRow_StyleChangeSplits verifies a style change mid-row produces two
// distinct spans.
func TestRenderRow_StyleChangeSplits(t *testing.T) {
	row := []sshterm.Cell{
		{Ch: 'A', Style: sshterm.Style{Bold: true, Fg: -1, Bg: -1}},
		{Ch: 'B', Style: sshterm.Style{Italic: true, Fg: -1, Bg: -1}},
	}
	got := renderRow(row, 2, -1)
	if !hasSGR(got, "1") || !hasSGR(got, "3") {
		t.Fatalf("expected both bold(1) and italic(3) SGRs, got %q", got)
	}
}

// TestRender_TopReverseHeaderEndToEnd is the full regression test for the
// reported issue: `top`'s PID/USER header row lost its reverse-video
// background. It feeds the parser the SGR sequences top emits (SGR 7m reverse
// on, header text, SGR 0 reset), then renders the row and asserts the output
// contains a reverse-video SGR (CSI 7m) — proving the style survived from
// parser → screen → renderer. Previously the renderer dropped all styles.
func TestRender_TopReverseHeaderEndToEnd(t *testing.T) {
	screen := sshterm.NewScreen(40)
	p := sshterm.NewParser(screen)
	// top enables reverse video, writes the header, then resets.
	p.Write([]byte("\x1b[7m  PID USER      PR  NI\x1b[0m"))

	row := screen.View(1)[0]
	got := renderRow(row, 40, -1)
	if !strings.Contains(got, "PID USER") {
		t.Fatalf("header text missing: %q", got)
	}
	if !hasSGR(got, "7") {
		t.Fatalf("reverse-video SGR (CSI 7m) missing from rendered header: %q", got)
	}
}

func TestRender_TopLocalizedHeaderKeepsReset(t *testing.T) {
	screen := sshterm.NewScreen(12)
	p := sshterm.NewParser(screen)
	p.Write([]byte("\x1b[7m进程号 USER\x1b[0m\r\nplain"))

	view := screen.View(2)
	header := renderRow(view[0], 12, -1)
	body := renderRow(view[1], 12, -1)
	if !strings.Contains(header, "\x1b[0m") {
		t.Fatalf("localized reverse header must keep reset sequence: %q", header)
	}
	if hasSGR(body, "7") {
		t.Fatalf("body row inherited reverse-video style: %q", body)
	}
}

func TestRenderRow_LocalizedHeaderDoesNotWrap(t *testing.T) {
	text := "进程号 USER"
	row := make([]sshterm.Cell, 12)
	i := 0
	for _, ch := range text {
		row[i] = sshterm.Cell{Ch: ch, Style: sshterm.Style{Rev: true, Fg: -1, Bg: -1}}
		i++
	}
	got := renderRow(row, 12, -1)
	if width := lipgloss.Width(got); width > 12 {
		t.Fatalf("localized header render width = %d, want <= 12; got %q", width, got)
	}
	if !strings.Contains(got, "\x1b[0m") {
		t.Fatalf("localized header must keep reset sequence: %q", got)
	}
}

// TestRenderRow_CursorDrawnAtColumn verifies the cursor is rendered as a
// reverse-video block at the requested column. With cursorCol=-1 there must be
// no reverse-video span; with cursorCol set, exactly one CSI 7m appears and it
// covers the cursor column (the cell before it resets).
func TestRenderRow_CursorDrawnAtColumn(t *testing.T) {
	// Row: "abc" with default style. Cursor at column 1 (the 'b').
	row := []sshterm.Cell{
		defStyled('a'), defStyled('b'), defStyled('c'),
	}

	// No cursor: plain text, no escapes.
	plain := renderRow(row, 3, -1)
	if plain != "abc" {
		t.Fatalf("plain render = %q, want \"abc\"", plain)
	}

	// Cursor at col 1: 'a' then reverse 'b' then plain 'c'.
	cur := renderRow(row, 3, 1)
	if !hasSGR(cur, "7") {
		t.Fatalf("cursor cell missing reverse-video (CSI 7m): %q", cur)
	}
	// 'a' should precede the reverse span (it's outside the cursor), and 'c'
	// should follow it.
	if !strings.HasPrefix(cur, "a") {
		t.Fatalf("expected 'a' before cursor span: %q", cur)
	}
	if !strings.HasSuffix(stripTrailingReset(cur), "c") {
		t.Fatalf("expected 'c' after cursor span: %q", cur)
	}
}

// stripTrailingReset removes a trailing CSI 0m if present so we can assert the
// last visible character.
func stripTrailingReset(s string) string {
	return strings.TrimSuffix(s, "\x1b[0m")
}

// TestRenderRow_CursorOverStyledCell verifies the cursor is rendered as a clean
// reverse-video block even over a styled cell (the cursor does not inherit the
// character's style; it is always a default-color reversed block, like a real
// terminal's cursor).
func TestRenderRow_CursorOverStyledCell(t *testing.T) {
	row := []sshterm.Cell{
		{Ch: 'X', Style: sshterm.Style{Bold: true, Fg: -1, Bg: -1}},
	}
	// Cursor on the bold X: reverse(7) present; the char renders under it.
	got := renderRow(row, 1, 0)
	if !hasSGR(got, "7") {
		t.Fatalf("cursor over styled cell must show reverse(7): %q", got)
	}
	if !strings.Contains(got, "X") {
		t.Fatalf("character under cursor missing: %q", got)
	}
	// Without the cursor, only bold, no reverse.
	noCur := renderRow(row, 1, -1)
	if !hasSGR(noCur, "1") || hasSGR(noCur, "7") {
		t.Fatalf("non-cursor bold cell should have bold but no reverse: %q", noCur)
	}
}

// TestRenderRow_CursorAtEndOfLine is the regression test for the bug where the
// cursor was invisible at the end of a line. Blank cells past the content have a
// zero-value style {Fg:0,Bg:0} (black); before the fix the reversed cursor was
// black-on-black and disappeared. Now the cursor cell uses default colors so the
// reversed block is always visible.
func TestRenderRow_CursorAtEndOfLine(t *testing.T) {
	// Row content is "ab" (2 cells); columns 2..4 are zero-value blank cells.
	row := []sshterm.Cell{
		defStyled('a'), defStyled('b'),
		// columns 2-4 left as zero-value Cell{} (Fg:0,Bg:0), like a real grid
	}
	// Cursor at col 2 (the blank cell right after 'b').
	got := renderRow(row, 5, 2)
	// The cursor cell must emit a reverse span that is NOT black-on-black.
	// A clean default+reverse cursor is exactly "\x1b[7m".
	if !strings.Contains(got, "\x1b[7m") {
		t.Fatalf("end-of-line cursor must emit a reverse span, got %q", got)
	}
	if strings.Contains(got, "\x1b[7;30;40m") {
		t.Fatalf("end-of-line cursor used black-on-black (invisible): %q", got)
	}
}

// TestRenderRow_CursorOnBlankGrid verifies the cursor is visible on a fully
// blank row (e.g. an empty prompt line where the cursor sits on a blank cell).
func TestRenderRow_CursorOnBlankGrid(t *testing.T) {
	// All-zero row (no content), cursor at col 0.
	got := renderRow(nil, 5, 0)
	if !strings.Contains(got, "\x1b[7m") {
		t.Fatalf("cursor on blank row must emit a reverse span, got %q", got)
	}
}
