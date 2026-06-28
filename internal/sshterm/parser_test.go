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
	out := make([]string, 0, s.Rows())
	for r := 0; r < s.Rows(); r++ {
		out = append(out, rowText(s.rows[r]))
	}
	return out
}

func TestParse_PlainText(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)
	p.Write([]byte("hello"))
	if got := rowText(s.rows[0]); got != "hello" {
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
	if got := rowText(s.rows[0]); got != "abcde" {
		t.Fatalf("row0 = %q, want abcde", got)
	}
	if got := rowText(s.rows[1]); got != "f" {
		t.Fatalf("row1 = %q, want f", got)
	}
}

func TestParse_Backspace(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)
	p.Write([]byte("abc\x08X")) // backspace then overwrite c with X
	if got := rowText(s.rows[0]); got != "abX" {
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
	if ch := s.rows[0][8].Ch; ch != 'b' {
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
	if got := rowText(s.rows[0]); got != "hell" {
		t.Fatalf("row0 = %q, want hell", got)
	}
}

func TestParse_CSI_ClearScreen(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)
	p.Write([]byte("line1\r\nline2\r\nline3"))
	p.Write([]byte("\x1b[2J")) // clear entire screen
	for r := 0; r < s.Rows(); r++ {
		if got := rowText(s.rows[r]); got != "" {
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
	if !s.rows[0][0].Style.Bold {
		t.Fatal("X should be bold")
	}
	if s.rows[0][1].Style.Bold {
		t.Fatal("Y should not be bold")
	}
}

func TestParse_SGR_Colors(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)
	// ESC[31m red fg, ESC[42m green bg
	p.Write([]byte("\x1b[31;42mA"))
	st := s.rows[0][0].Style
	if st.Fg != 1 { // ANSI 30+x maps to code x (1=red)
		t.Fatalf("fg = %d, want 1 (red)", st.Fg)
	}
	if st.Bg != 2 { // ANSI 40+x maps to code x (2=green)
		t.Fatalf("bg = %d, want 2 (green)", st.Bg)
	}
}

func TestParse_IgnoresUnknownCSI(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)
	// Unknown CSI should be consumed without breaking subsequent text.
	p.Write([]byte("a\x1b[99Za"))
	if got := rowText(s.rows[0]); got != "aa" {
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
	got := rowText(s.rows[0])
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
		if got := rowText(s.rows[0]); got != "X" {
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
	got := rowText(s.rows[0])
	if got != "B" {
		t.Fatalf("row0 = %q, want B", got)
	}
	if !s.rows[0][0].Style.Bold {
		t.Fatal("B should be bold — public SGR after private sequence was mis-parsed")
	}
}

func TestParse_UTF8(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)
	p.Write([]byte("héllo")) // é is 2 bytes in UTF-8
	if got := rowText(s.rows[0]); got != "héllo" {
		t.Fatalf("row0 = %q, want héllo", got)
	}
	r, c := s.Cursor()
	if r != 0 || c != 5 {
		t.Fatalf("cursor = (%d,%d), want (0,5)", r, c)
	}
}
