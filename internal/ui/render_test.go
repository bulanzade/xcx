package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

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

// TestRender_ExtendedColorEndToEnd is the full regression test for the
// monochrome AI-agent bug: codex/pi style TUIs color their interface almost
// exclusively with SGR 38;5;n (256-color) and 38;2;r;g;b (truecolor), which the
// parser used to discard. The sequences must survive parser → screen →
// renderer and be re-emitted to the host terminal.
func TestRender_ExtendedColorEndToEnd(t *testing.T) {
	screen := sshterm.NewScreen(40)
	p := sshterm.NewParser(screen)
	p.Write([]byte("\x1b[38;5;208magent\x1b[0m \x1b[38;2;255;100;0mprompt\x1b[0m"))

	row := screen.View(1)[0]
	got := renderRow(row, 40, -1)
	if !strings.Contains(got, "agent") || !strings.Contains(got, "prompt") {
		t.Fatalf("text missing: %q", got)
	}
	if !strings.Contains(got, "\x1b[38;5;208m") {
		t.Fatalf("256-color SGR missing: %q", got)
	}
	if !strings.Contains(got, "\x1b[38;2;255;100;0m") {
		t.Fatalf("truecolor SGR missing: %q", got)
	}
}

// TestRenderRow_BrightAndExtended verifies the renderer maps bright palette
// indexes (8-15) to the 9x/10x SGR form and palette 16-255 to ext;5;n, for
// both foreground and background.
func TestRenderRow_BrightAndExtended(t *testing.T) {
	row := []sshterm.Cell{
		{Ch: 'A', Style: sshterm.Style{Fg: 9, Bg: -1}},   // bright red fg -> 91
		{Ch: 'B', Style: sshterm.Style{Fg: -1, Bg: 196}}, // palette bg -> 48;5;196
	}
	got := renderRow(row, 2, -1)
	if !hasSGR(got, "91") {
		t.Fatalf("bright-red fg SGR (CSI 91m) missing: %q", got)
	}
	if !strings.Contains(got, "48;5;196") {
		t.Fatalf("256-color bg SGR missing: %q", got)
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

// TestRenderRow_AmbiguousWidthLocaleIndependent is the regression test for the
// "pi input box shows only its left half" bug on Chinese-locale hosts: box
// drawing characters (─) and bullets (•) are East Asian Ambiguous, which
// go-runewidth counts as TWO columns when the local locale is zh/ja/ko. The
// remote program laid those lines out one column per cell, so the renderer
// must too — a full-width rule of 100 cells was cut to 50 and right-aligned
// footer text was clipped. Forcing the CJK width mode here makes the test
// fail on any locale without the fix.
func TestRenderRow_AmbiguousWidthLocaleIndependent(t *testing.T) {
	prev := runewidth.DefaultCondition.EastAsianWidth
	runewidth.DefaultCondition.EastAsianWidth = true
	defer func() { runewidth.DefaultCondition.EastAsianWidth = prev }()
	if w := runewidth.RuneWidth('─'); w != 2 {
		t.Fatalf("test setup: ─ measures %d columns under EastAsianWidth, want 2", w)
	}

	// A full-width rule (pi's input box border) must render every cell.
	row := make([]sshterm.Cell, 100)
	for i := range row {
		row[i] = sshterm.Cell{Ch: '─', Style: sshterm.Style{Fg: 104}}
	}
	got := renderRow(row, 100, -1)
	if n := strings.Count(got, "─"); n != 100 {
		t.Fatalf("rendered %d of 100 box-drawing cells; ambiguous width halved the line", n)
	}

	// Right-aligned footer text after an ambiguous bullet must not be clipped.
	text := strings.Repeat("a", 94) + " • " + "high"
	row2 := make([]sshterm.Cell, len(text))
	for i, ch := range text {
		row2[i] = sshterm.Cell{Ch: ch, Style: sshterm.Style{Fg: -1, Bg: -1}}
	}
	got2 := renderRow(row2, len(text), -1)
	if !strings.Contains(got2, "high") {
		t.Fatalf("text after ambiguous-width bullet was clipped: %q", got2)
	}

	// Genuinely wide runes still take two columns (this is layout, not the bug).
	row3 := []sshterm.Cell{{Ch: '漢', Style: sshterm.Style{Fg: -1, Bg: -1}}, defStyled('a'), defStyled('b')}
	got3 := renderRow(row3, 3, -1)
	if !strings.Contains(got3, "漢") || strings.Contains(got3, "ab") {
		t.Fatalf("wide rune should consume two columns and clip the tail: %q", got3)
	}
}

// TestRenderRow_ReverseKeepsColorParams is the regression test for the
// double-inverted reverse video: sgrOn used to swap fg/bg itself AND emit
// SGR 7, so the host terminal swapped again — ESC[7;31m (red background on
// xterm) showed as red TEXT on a default background. The params must be
// re-emitted as parsed; the host's SGR 7 performs the swap.
func TestRenderRow_ReverseKeepsColorParams(t *testing.T) {
	screen := sshterm.NewScreen(40)
	p := sshterm.NewParser(screen)
	p.Write([]byte("\x1b[7;31mX\x1b[0m"))

	got := renderRow(screen.View(1)[0], 40, -1)
	if !strings.Contains(got, "7;31") {
		t.Fatalf("reverse+red must re-emit the parsed params 7;31: %q", got)
	}
	if strings.Contains(got, "7;41") || strings.Contains(got, "7;34") {
		t.Fatalf("reverse video must not swap color params before emitting: %q", got)
	}
}

// TestRenderRow_ZeroCellsRenderPlain pins the filler-cell rule: unwritten
// grid cells (Ch==0 zero values) render as plain blanks, never the
// black-on-black 30;40 padding they carried before.
func TestRenderRow_ZeroCellsRenderPlain(t *testing.T) {
	got := renderRow(make([]sshterm.Cell, 5), 5, -1)
	if got != "     " {
		t.Fatalf("zero-value row = %q, want five plain spaces", got)
	}
	if strings.Contains(got, "30;40") || strings.Contains(got, "\x1b") {
		t.Fatalf("zero-value cells must not emit SGR: %q", got)
	}
}
