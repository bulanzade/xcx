package sshterm

import (
	"strings"
	"testing"
)

// rowText returns the printable content of a row, trimming trailing blanks.
func rowText(row []Cell) string {
	out := make([]rune, 0, len(row))
	last := -1
	for i, c := range row {
		if c.Ch != 0 {
			last = i
		}
	}
	for i := 0; i <= last; i++ {
		if row[i].Ch == 0 {
			out = append(out, ' ')
		} else {
			out = append(out, row[i].Ch)
		}
	}
	return string(out)
}

// screenLines returns the trimmed text of every row.
func screenLines(s *Screen) []string {
	rows := screenRows(s)
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, rowText(row))
	}
	return out
}

func screenRows(s *Screen) [][]Cell { return s.View(s.Rows()) }

func TestParse_PlainText(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)
	p.Write([]byte("hello"))
	if got := rowText(screenRows(s)[0]); got != "hello" {
		t.Fatalf("row0 = %q, want hello", got)
	}
	r, c := s.Cursor()
	if r != 0 || c != 5 {
		t.Fatalf("cursor = (%d,%d), want (0,5)", r, c)
	}
}

func TestParse_CRLF(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)
	p.Write([]byte("ab\r\ncd"))
	lines := screenLines(s)
	// row0=ab, row1=cd
	if len(lines) < 2 || lines[0] != "ab" || lines[1] != "cd" {
		t.Fatalf("lines = %v", lines)
	}
}

func TestParse_LineWrap(t *testing.T) {
	s := NewScreen(5)
	p := NewParser(s)
	p.Write([]byte("abcdef")) // 6 chars into width 5 -> wraps
	if got := rowText(screenRows(s)[0]); got != "abcde" {
		t.Fatalf("row0 = %q, want abcde", got)
	}
	if got := rowText(screenRows(s)[1]); got != "f" {
		t.Fatalf("row1 = %q, want f", got)
	}
}

func TestParse_Backspace(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)
	p.Write([]byte("abc\x08X")) // backspace then overwrite c with X
	if got := rowText(screenRows(s)[0]); got != "abX" {
		t.Fatalf("row0 = %q, want abX", got)
	}
}

func TestParse_Tab(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)
	p.Write([]byte("a\tb"))
	// col after 'a' is 1; tab -> col 8; 'b' at col 8
	r, c := s.Cursor()
	if r != 0 || c != 9 {
		t.Fatalf("cursor = (%d,%d), want (0,9)", r, c)
	}
	if ch := screenRows(s)[0][8].Ch; ch != 'b' {
		t.Fatalf("char at col 8 = %q, want b", ch)
	}
}

func TestParse_CSI_ClearLine(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)
	p.Write([]byte("hello world"))
	// move cursor to col 5 ("hello") then clear to end of line: "hello"
	p.Write([]byte("\x1b[5G")) // CSI 5 G = cursor to column 5 (1-based) -> col 4
	p.Write([]byte("\x1b[K"))  // CSI K = erase cursor to end of line
	if got := rowText(screenRows(s)[0]); got != "hell" {
		t.Fatalf("row0 = %q, want hell", got)
	}
}

func TestParse_CSI_ClearScreen(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)
	p.Write([]byte("line1\r\nline2\r\nline3"))
	p.Write([]byte("\x1b[2J")) // clear entire screen
	for r := 0; r < s.Rows(); r++ {
		if got := rowText(screenRows(s)[r]); got != "" {
			t.Fatalf("row %d not cleared: %q", r, got)
		}
	}
}

// TestParse_ClearReanchorsView is a regression test for the bug where, after
// `clear`, the screen looked blank until several more commands scrolled the
// cursor back into view. `clear` emits CSI H (home) then CSI 2J (clear). The
// old ClearScreen wiped cell contents but left the scrollback rows in place,
// so the post-clear prompt written at row 0 sat above the View window (which
// shows the BOTTOM h rows). With the fix, clear truncates scrollback so the
// new prompt is immediately visible.
func TestParse_ClearReanchorsView(t *testing.T) {
	s := NewScreen(40)
	p := NewParser(s)
	// Build up scrollback so row count exceeds a typical view height.
	for i := 0; i < 25; i++ {
		p.Write([]byte("line\r\n"))
	}
	// Now `clear`: home + clear screen.
	p.Write([]byte("\x1b[H\x1b[2J"))
	// A fresh prompt is written at the top.
	p.Write([]byte("root@host:~# "))

	// A 10-row view should show the prompt (not blank lines from old scrollback).
	view := s.View(10)
	var visible []string
	for _, row := range view {
		visible = append(visible, rowText(row))
	}
	found := false
	for _, l := range visible {
		if strings.HasPrefix(l, "root@host:~#") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("post-clear prompt not visible in 10-row view; visible=%q", visible)
	}
}

// TestParse_Clear3JClearsScrollback verifies CSI 3J (clear screen + scrollback)
// also truncates so subsequent output is visible.
func TestParse_Clear3JClearsScrollback(t *testing.T) {
	s := NewScreen(40)
	p := NewParser(s)
	for i := 0; i < 20; i++ {
		p.Write([]byte("x\r\n"))
	}
	p.Write([]byte("\x1b[H\x1b[3J"))
	p.Write([]byte("after"))
	view := s.View(8)
	var visible []string
	for _, row := range view {
		visible = append(visible, rowText(row))
	}
	found := false
	for _, l := range visible {
		if l == "after" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("post-3J output not visible; visible=%q", visible)
	}
}

func TestParse_CSI_CursorMove(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)
	p.Write([]byte("XY"))
	// CSI 1 ; 1 H sets cursor to row 1 col 1 (1-based) -> (0,0)
	p.Write([]byte("\x1b[1;1H"))
	r, c := s.Cursor()
	if r != 0 || c != 0 {
		t.Fatalf("cursor after H = (%d,%d), want (0,0)", r, c)
	}
	// relative: CSI 1 B = down 1, CSI 2 C = right 2
	p.Write([]byte("\x1b[1B\x1b[2C"))
	r, c = s.Cursor()
	if r != 1 || c != 2 {
		t.Fatalf("cursor after B/C = (%d,%d), want (1,2)", r, c)
	}
}

func TestParse_SGR_Style(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)
	// ESC[1m bold, write X, ESC[0m reset, write Y
	p.Write([]byte("\x1b[1mX\x1b[0mY"))
	if !screenRows(s)[0][0].Style.Bold {
		t.Fatal("X should be bold")
	}
	if screenRows(s)[0][1].Style.Bold {
		t.Fatal("Y should not be bold")
	}
}

func TestParse_SGR_Colors(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)
	// ESC[31m red fg, ESC[42m green bg
	p.Write([]byte("\x1b[31;42mA"))
	st := screenRows(s)[0][0].Style
	if st.Fg != 1 { // ANSI 30+x maps to code x (1=red)
		t.Fatalf("fg = %d, want 1 (red)", st.Fg)
	}
	if st.Bg != 2 { // ANSI 40+x maps to code x (2=green)
		t.Fatalf("bg = %d, want 2 (green)", st.Bg)
	}
}

// TestParse_SGR_256Color is a regression test for the bug where SGR 38;5;n /
// 48;5;n were consumed but never applied, rendering 256-color TUIs (codex, pi,
// bubbletea apps) monochrome. Both forms must set real palette colors.
func TestParse_SGR_256Color(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)
	p.Write([]byte("\x1b[38;5;196mA\x1b[48;5;21mB"))
	st := screenRows(s)[0][0].Style
	if st.Fg.IsRGB() || st.Fg.PaletteIndex() != 196 {
		t.Fatalf("fg = %v, want palette 196", st.Fg)
	}
	st = screenRows(s)[0][1].Style
	if st.Bg.IsRGB() || st.Bg.PaletteIndex() != 21 {
		t.Fatalf("bg = %v, want palette 21", st.Bg)
	}
	if st.Fg.PaletteIndex() != 196 {
		t.Fatalf("fg after bg change = %v, want palette 196", st.Fg)
	}
}

// TestParse_SGR_TrueColor is a regression test for the bug where SGR 38;2;r;g;b
// / 48;2;r;g;b (direct 24-bit color) were dropped, so truecolor interfaces lost
// all color.
func TestParse_SGR_TrueColor(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)
	p.Write([]byte("\x1b[38;2;255;100;0mA\x1b[48;2;1;2;3mB"))
	st := screenRows(s)[0][0].Style
	if !st.Fg.IsRGB() {
		t.Fatalf("fg = %v, want direct RGB", st.Fg)
	}
	if r, g, b := st.Fg.RGB(); r != 255 || g != 100 || b != 0 {
		t.Fatalf("fg rgb = (%d,%d,%d), want (255,100,0)", r, g, b)
	}
	st = screenRows(s)[0][1].Style
	if r, g, b := st.Bg.RGB(); !st.Bg.IsRGB() || r != 1 || g != 2 || b != 3 {
		t.Fatalf("bg rgb = (%d,%d,%d), want (1,2,3)", r, g, b)
	}
}

// TestParse_SGR_BrightColors verifies aixterm bright codes 90-97/100-107 map
// to palette indexes 8-15.
func TestParse_SGR_BrightColors(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)
	p.Write([]byte("\x1b[91mA\x1b[103mB"))
	st := screenRows(s)[0][0].Style
	if st.Fg.PaletteIndex() != 9 || st.Fg.IsRGB() {
		t.Fatalf("fg = %v, want palette 9 (bright red)", st.Fg)
	}
	st = screenRows(s)[0][1].Style
	if st.Bg.PaletteIndex() != 11 || st.Bg.IsRGB() {
		t.Fatalf("bg = %v, want palette 11 (bright yellow)", st.Bg)
	}
}

// TestParse_SGR_ColonForm verifies the colon-separated SGR form (38:5:n) is
// consumed as a sub-parameter list instead of aborting the sequence — before
// the fix the parser reset on ':' and leaked "5:196m" as visible text.
func TestParse_SGR_ColonForm(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)
	p.Write([]byte("\x1b[38:5:196mX"))
	if got := rowText(screenRows(s)[0]); got != "X" {
		t.Fatalf("row0 = %q, want X (no leaked digits)", got)
	}
	if fg := screenRows(s)[0][0].Style.Fg; fg.PaletteIndex() != 196 {
		t.Fatalf("fg = %v, want palette 196", fg)
	}
}

// TestParse_SGR_ExtendedMalformed verifies truncated/invalid extended-color
// forms do not desync the parser: subsequent parameters in the same or
// following sequences still apply.
func TestParse_SGR_ExtendedMalformed(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)
	// 38 without a mode, 38;2 missing blue, 38;5;999 out of palette range.
	p.Write([]byte("\x1b[38mA\x1b[38;2;255;0mB\x1b[38;5;999mC\x1b[31mD"))
	if got := rowText(screenRows(s)[0]); got != "ABCD" {
		t.Fatalf("row0 = %q, want ABCD", got)
	}
	if fg := screenRows(s)[0][3].Style.Fg; fg != 1 {
		t.Fatalf("fg after malformed forms = %v, want 1 (red)", fg)
	}
}

// TestParse_SGR_ExtendedReset verifies SGR 0 clears extended colors set via
// 38;5 / 38;2 back to the terminal default.
func TestParse_SGR_ExtendedReset(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)
	p.Write([]byte("\x1b[38;2;10;20;30mA\x1b[0mB"))
	st := screenRows(s)[0][0].Style
	if !st.Fg.IsRGB() {
		t.Fatalf("fg before reset = %v, want RGB", st.Fg)
	}
	if fg := screenRows(s)[0][1].Style.Fg; fg != ColorDefault {
		t.Fatalf("fg after reset = %v, want default", fg)
	}
}

func TestParse_IgnoresUnknownCSI(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)
	// Unknown CSI should be consumed without breaking subsequent text.
	p.Write([]byte("a\x1b[99Za"))
	if got := rowText(screenRows(s)[0]); got != "aa" {
		t.Fatalf("row0 = %q, want aa", got)
	}
}

// TestParse_PrivateModeSequences is a regression test for the bug where DEC
// private mode sequences leaked as visible text. Real shells emit, around every
// prompt, "\x1b[?2004h" (enable bracketed paste) and "\x1b[?2004l" before a
// command. The '?' is a private-parameter prefix; before the fix the parser
// aborted the sequence on '?' and printed "2004h" on screen.
func TestParse_PrivateModeSequences(t *testing.T) {
	s := NewScreen(40)
	p := NewParser(s)
	// Simulate a prompt bracketed by private-mode set/reset. The key assertion
	// is that "2004h"/"2004l" do NOT appear in the rendered row; the prompt
	// text itself is preserved verbatim.
	p.Write([]byte("\x1b[?2004hroot@host:~#\x1b[?2004l"))
	got := rowText(screenRows(s)[0])
	if got != "root@host:~#" {
		t.Fatalf("row0 = %q, want %q (private sequences leaked)", got, "root@host:~#")
	}
}

// TestParse_CommonDECModes verifies the frequently-seen private sequences are
// all consumed rather than drawn: ?25 cursor visibility, ?1049/?47 alt screen,
// ?1 cursor-key mode, ?2004 bracketed paste.
func TestParse_CommonDECModes(t *testing.T) {
	seqs := []string{
		"\x1b[?25h",   // show cursor
		"\x1b[?25l",   // hide cursor
		"\x1b[?1049h", // enter alt screen
		"\x1b[?1049l", // leave alt screen
		"\x1b[?47h",   // alt screen (legacy)
		"\x1b[?1h",    // cursor key mode
		"\x1b[?2004h", // bracketed paste on
		"\x1b[?2004l", // bracketed paste off
		"\x1b[?7h",    // auto-wrap on
	}
	for _, seq := range seqs {
		s := NewScreen(20)
		p := NewParser(s)
		p.Write([]byte(seq + "X"))
		if got := rowText(screenRows(s)[0]); got != "X" {
			t.Errorf("after %q row0 = %q, want X (sequence leaked)", seq, got)
		}
	}
}

// TestParse_PrivateThenPublic verifies a private sequence does not poison the
// parser state for the following public sequence (e.g. SGR color reset).
func TestParse_PrivateThenPublic(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)
	p.Write([]byte("\x1b[?2004h\x1b[1mB\x1b[0m"))
	got := rowText(screenRows(s)[0])
	if got != "B" {
		t.Fatalf("row0 = %q, want B", got)
	}
	if !screenRows(s)[0][0].Style.Bold {
		t.Fatal("B should be bold — public SGR after private sequence was mis-parsed")
	}
}

func TestParse_UTF8(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)
	p.Write([]byte("héllo")) // é is 2 bytes in UTF-8
	if got := rowText(screenRows(s)[0]); got != "héllo" {
		t.Fatalf("row0 = %q, want héllo", got)
	}
	r, c := s.Cursor()
	if r != 0 || c != 5 {
		t.Fatalf("cursor = (%d,%d), want (0,5)", r, c)
	}
}

// TestParse_MouseReportingModes is the regression test for htop's mouse
// support: htop enables reporting with the combined-parameter sequence
// CSI ? 1006;1000 h, and the parser used to examine only the FIRST DEC mode
// parameter — the 1000 tracking request was silently dropped, so mouse events
// never reached the remote program. All modes must be tracked across
// combined and separate sequences, and reset by their matching 'l'.
func TestParse_MouseReportingModes(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)

	p.Write([]byte("\x1b[?1006;1000h")) // htop's enable form
	cfg := p.MouseConfig()
	if !cfg.Reporting || !cfg.SGR || cfg.Motion != 0 {
		t.Fatalf("after ?1006;1000h: cfg=%+v, want Reporting+SGR, no motion", cfg)
	}

	p.Write([]byte("\x1b[?1006;1000l")) // htop's disable form
	if cfg = p.MouseConfig(); cfg.Reporting || cfg.SGR {
		t.Fatalf("after reset: cfg=%+v, want reporting off", cfg)
	}

	p.Write([]byte("\x1b[?1003h")) // any-event tracking (vim/pi)
	if cfg = p.MouseConfig(); cfg.Motion != 2 {
		t.Fatalf("after ?1003h: Motion=%d, want 2", cfg.Motion)
	}
	p.Write([]byte("\x1b[?1003l"))
	p.Write([]byte("\x1b[?1002h")) // button-event tracking
	if cfg = p.MouseConfig(); cfg.Motion != 1 {
		t.Fatalf("after ?1002h: Motion=%d, want 1", cfg.Motion)
	}
	p.Write([]byte("\x1b[?9h")) // X10 press-only
	if cfg = p.MouseConfig(); !cfg.X10 {
		t.Fatal("after ?9h: X10 not set")
	}
}

// TestParse_MouseTrackingModesReplaceEachOther pins the tracking-mode group
// semantics: ?9/?1000/?1002/?1003 select one protocol, not additive flags.
// Enabling a mode replaces the active one; disabling clears reporting. As
// additive bits, a stale ?1003 survived "enable ?1003, enable ?1000, disable
// ?1000" and kept the pane swallowing the mouse after the remote program
// turned reporting off. Encoding flags (?1006) stay independent of the group.
func TestParse_MouseTrackingModesReplaceEachOther(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)

	p.Write([]byte("\x1b[?1003h"))
	if cfg := p.MouseConfig(); !cfg.Reporting || cfg.Motion != 2 {
		t.Fatalf("after ?1003h: cfg=%+v, want Reporting with Motion=2", cfg)
	}
	p.Write([]byte("\x1b[?1000h")) // replaces ?1003
	if cfg := p.MouseConfig(); !cfg.Reporting || cfg.Motion != 0 {
		t.Fatalf("after ?1000h over ?1003: cfg=%+v, want Reporting with Motion=0", cfg)
	}
	p.Write([]byte("\x1b[?1000l")) // disabling the active mode clears the group
	if cfg := p.MouseConfig(); cfg.Reporting {
		t.Fatalf("after ?1000l: cfg=%+v, want reporting off", cfg)
	}
	p.Write([]byte("\x1b[?1006h\x1b[?1002h"))
	if cfg := p.MouseConfig(); !cfg.SGR || cfg.Motion != 1 {
		t.Fatalf("after ?1006h ?1002h: cfg=%+v, want SGR with Motion=1", cfg)
	}
}

// TestParse_CombinedDECPrivateModes verifies every parameter of a multi-mode
// CSI ? ... h sequence takes effect, not just the first one.
func TestParse_CombinedDECPrivateModes(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)
	p.Write([]byte("\x1b[?2004;1h")) // bracketed paste + application cursor
	if !p.BracketedPaste() {
		t.Fatal("bracketed paste (param 2) was dropped from combined DEC mode set")
	}
	if !p.ApplicationCursor() {
		t.Fatal("application cursor (param 1) was dropped from combined DEC mode set")
	}
	p.Write([]byte("\x1b[?1;2004l"))
	if p.BracketedPaste() || p.ApplicationCursor() {
		t.Fatal("combined DEC mode reset did not clear both modes")
	}
}

// TestParse_SGR_SubParams pins the colon sub-parameter semantics: a
// sub-parameter belongs to its owning code and must never be reinterpreted as
// an independent SGR code. Before the sub-flag tracking, 4:3 (neovim's
// undercurl) became underline+italic, 4:0 performed a full reset, kitty's
// 38:2::r:g:b colorspace form computed a wrong color and let the trailing
// channel clobber the style, and 58 underline-color arguments leaked as SGR
// codes (flattening an empty slot to 0 = reset).
func TestParse_SGR_SubParams(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)
	p.Write([]byte("\x1b[4:3mA\x1b[0m\x1b[4:0mB"))
	stA := screenRows(s)[0][0].Style
	if !stA.Under || stA.Italic {
		t.Fatalf("4:3 -> %+v, want underline without italic", stA)
	}
	if stB := screenRows(s)[0][1].Style; stB.Under {
		t.Fatalf("4:0 -> %+v, want underline off (not a full reset either)", stB)
	}

	// kitty colorspace form: the empty slot after :: is skipped, and the
	// trailing blue channel does NOT leak as an SGR code.
	s2 := NewScreen(20)
	p2 := NewParser(s2)
	p2.Write([]byte("\x1b[1m\x1b[38:2::10:20:30mX\x1b[0m\x1b[58:2::1:2:3mY"))
	st := screenRows(s2)[0][0].Style
	if r, g, b := st.Fg.RGB(); !st.Fg.IsRGB() || r != 10 || g != 20 || b != 30 {
		t.Fatalf("38:2::10:20:30 fg = %v, want RGB(10,20,30)", st.Fg)
	}
	if !st.Bold {
		t.Fatal("bold set before the colon sequence was lost")
	}
	stY := screenRows(s2)[0][1].Style
	if stY.Fg != ColorDefault || stY.Bg != ColorDefault || stY.Dim {
		t.Fatalf("58:2::… must not touch style (or reset it): %+v", stY)
	}

	// Semicolon forms are unaffected: 4;3 stays underline+italic.
	s3 := NewScreen(20)
	p3 := NewParser(s3)
	p3.Write([]byte("\x1b[4;3mZ"))
	if st := screenRows(s3)[0][0].Style; !st.Under || !st.Italic {
		t.Fatalf("4;3 -> %+v, want underline AND italic", st)
	}
}

// TestParse_SGR_ColorspaceGroupBoundary pins the colon-group boundary in the
// truecolor parser: 38:2:0:128:64 holds only three group values, so those
// are the channels — the colorspace slot is skipped only when the group
// carries four values (colorspace + 3 channels). Reading past the group's
// end used to swallow the following ';'-separated attribute, turning
// ESC[38:2:0:128:64;1m into RGB(128,64,1) without bold instead of
// RGB(0,128,64) with bold.
func TestParse_SGR_ColorspaceGroupBoundary(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)
	p.Write([]byte("\x1b[38:2:0:128:64;1mX"))
	st := screenRows(s)[0][0].Style
	if r, g, b := st.Fg.RGB(); !st.Fg.IsRGB() || r != 0 || g != 128 || b != 64 {
		t.Fatalf("38:2:0:128:64 fg = %v, want RGB(0,128,64)", st.Fg)
	}
	if !st.Bold {
		t.Fatal("the ;1 after the color group was consumed; bold lost")
	}

	// A four-value group is the colorspace form: the leading slot is skipped.
	s2 := NewScreen(20)
	p2 := NewParser(s2)
	p2.Write([]byte("\x1b[38:2:1:10:20:30mY"))
	st2 := screenRows(s2)[0][0].Style
	if r, g, b := st2.Fg.RGB(); !st2.Fg.IsRGB() || r != 10 || g != 20 || b != 30 {
		t.Fatalf("38:2:1:10:20:30 fg = %v, want RGB(10,20,30) with colorspace 1 skipped", st2.Fg)
	}
}

// TestParse_SGR_ExtendedCombinedWithAttributes verifies the extended-color
// consumption indices when attributes and colors share one sequence — the
// off-by-one-prone core of the color fix.
func TestParse_SGR_ExtendedCombinedWithAttributes(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)
	p.Write([]byte("\x1b[1;38;2;10;20;30;4mX"))
	st := screenRows(s)[0][0].Style
	if !st.Bold || !st.Under || !st.Fg.IsRGB() {
		t.Fatalf("1;38;2;10;20;30;4 -> %+v, want bold+underline+RGB", st)
	}

	s2 := NewScreen(20)
	p2 := NewParser(s2)
	p2.Write([]byte("\x1b[38;5;196;1mY"))
	st2 := screenRows(s2)[0][0].Style
	if !st2.Bold || st2.Fg.PaletteIndex() != 196 || st2.Fg.IsRGB() {
		t.Fatalf("38;5;196;1 -> %+v, want bold + palette 196", st2)
	}

	// Truncated truecolor: missing channels default to 0 and are consumed —
	// the leftover digits must not resurface as SGR codes (a stray 0 reset
	// used to wipe previously-set attributes).
	s3 := NewScreen(20)
	p3 := NewParser(s3)
	p3.Write([]byte("\x1b[1m\x1b[38;2;255;0mZ"))
	st3 := screenRows(s3)[0][0].Style
	if !st3.Bold {
		t.Fatal("truncated 38;2;255;0 leaked a reset that cleared bold")
	}
	if r, g, b := st3.Fg.RGB(); !st3.Fg.IsRGB() || r != 255 || g != 0 || b != 0 {
		t.Fatalf("truncated 38;2;255;0 fg = %v, want RGB(255,0,0)", st3.Fg)
	}
}
