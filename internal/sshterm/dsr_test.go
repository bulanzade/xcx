package sshterm

import (
	"bytes"
	"testing"
)

// captureResponder returns a response callback that appends reply bytes to a
// buffer, plus the buffer to read them back.
func captureResponder() (func([]byte), *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return func(b []byte) { buf.Write(b) }, buf
}

// TestDSR_CursorPositionReport is the core regression test for the bug where
// vim (and other full-screen apps) showed stray digits like "10;?11;?" on a
// blank file. vim sends CSI 6 n (report cursor position); a real terminal must
// reply with CSI Pl ; Pc R. Before the fix the terminal never replied, so the
// app kept waiting/reparsing and rendered artifacts.
func TestDSR_CursorPositionReport(t *testing.T) {
	s := NewScreen(40)
	s.SetCursor(9, 10) // 0-based row 9, col 10
	p := NewParser(s)
	cb, buf := captureResponder()
	p.SetResponder(cb)

	p.Write([]byte("\x1b[6n"))
	got := buf.String()
	want := "\x1b[10;11R" // 1-based: row 10, col 11
	if got != want {
		t.Fatalf("DSR cursor response = %q, want %q", got, want)
	}
}

// TestDSR_DeviceStatus verifies CSI 5 n (status) replies with CSI 0 n.
func TestDSR_DeviceStatus(t *testing.T) {
	s := NewScreen(40)
	p := NewParser(s)
	cb, buf := captureResponder()
	p.SetResponder(cb)
	p.Write([]byte("\x1b[5n"))
	if got := buf.String(); got != "\x1b[0n" {
		t.Fatalf("DSR status response = %q, want \"\\x1b[0n\"", got)
	}
}

// TestDA_Primary verifies CSI c (primary DA) replies with a VT-style capability
// string.
func TestDA_Primary(t *testing.T) {
	s := NewScreen(40)
	p := NewParser(s)
	cb, buf := captureResponder()
	p.SetResponder(cb)
	p.Write([]byte("\x1b[c"))
	got := buf.String()
	if len(got) == 0 || got[0] != 0x1b {
		t.Fatalf("primary DA should reply with an ESC sequence, got %q", got)
	}
}

// TestDA_Secondary verifies CSI > c (secondary DA) is answered (vim sends this).
func TestDA_Secondary(t *testing.T) {
	s := NewScreen(40)
	p := NewParser(s)
	cb, buf := captureResponder()
	p.SetResponder(cb)
	p.Write([]byte("\x1b[>c"))
	got := buf.String()
	if got != "\x1b[>0;0;0c" {
		t.Fatalf("secondary DA response = %q, want \"\\x1b[>0;0;0c\"", got)
	}
}

// TestDSR_NoResponderNoLeak verifies that when no responder is wired, query
// sequences are still consumed (not rendered as text) — so they can't leak
// digits even before the Terminal is attached.
func TestDSR_NoResponderNoLeak(t *testing.T) {
	s := NewScreen(40)
	p := NewParser(s) // no SetResponder
	p.Write([]byte("\x1b[6n\x1b[5n\x1b[c\x1b[>c"))
	if got := rowText(screenRows(s)[0]); got != "" {
		t.Fatalf("queries leaked as text with no responder: %q", got)
	}
}

// TestDSR_ResponseUsesLiveCursor verifies the cursor report reflects cursor
// moves, not a fixed position.
func TestDSR_ResponseUsesLiveCursor(t *testing.T) {
	s := NewScreen(40)
	p := NewParser(s)
	cb, buf := captureResponder()
	p.SetResponder(cb)
	// move cursor to row 0 col 4 via text + CR
	p.Write([]byte("abcd"))
	p.Write([]byte("\x1b[6n"))
	if got := buf.String(); got != "\x1b[1;5R" {
		t.Fatalf("live cursor report = %q, want \"\\x1b[1;5R\"", got)
	}
}
