package sshterm

import "testing"

func TestTextRangeSingleAndMultiLine(t *testing.T) {
	s := NewScreen(12)
	p := NewParser(s)
	p.Write([]byte("hello world\r\nsecond line"))

	if got := s.TextRange(2, Point{Row: 0, Col: 0}, Point{Row: 0, Col: 4}); got != "hello" {
		t.Fatalf("single line = %q, want hello", got)
	}
	if got := s.TextRange(2, Point{Row: 0, Col: 6}, Point{Row: 1, Col: 5}); got != "world\nsecond" {
		t.Fatalf("multi line = %q, want world\\nsecond", got)
	}
}

func TestTextRangeReverseAndClamp(t *testing.T) {
	s := NewScreen(5)
	p := NewParser(s)
	p.Write([]byte("abcde\r\n12345"))

	got := s.TextRange(2, Point{Row: 10, Col: 10}, Point{Row: -1, Col: -1})
	if got != "abcde\n12345" {
		t.Fatalf("reverse clamp = %q, want full visible text", got)
	}
}

func TestTextRangeTrimsLineRightPadding(t *testing.T) {
	s := NewScreen(8)
	p := NewParser(s)
	p.Write([]byte("abc"))

	if got := s.TextRange(1, Point{Row: 0, Col: 0}, Point{Row: 0, Col: 7}); got != "abc" {
		t.Fatalf("trim padding = %q, want abc", got)
	}
}

func TestParserTracksBracketedPaste(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)
	if p.BracketedPaste() {
		t.Fatal("bracketed paste should default off")
	}
	p.Write([]byte("\x1b[?2004h"))
	if !p.BracketedPaste() {
		t.Fatal("bracketed paste was not enabled")
	}
	p.Write([]byte("\x1b[?2004l"))
	if p.BracketedPaste() {
		t.Fatal("bracketed paste was not disabled")
	}
}
