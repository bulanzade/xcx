package sshterm

import "testing"

// TestCursorInView verifies the cursor position is correctly translated into
// the View(height) coordinate space, including the scrolled-back case where the
// cursor sits above the visible window.
func TestCursorInView(t *testing.T) {
	s := NewScreen(40)
	p := NewParser(s)
	// Build several lines of scrollback: rows 0..4 each "line".
	for i := 0; i < 5; i++ {
		p.Write([]byte("line\r\n"))
	}
	// After 5 "line\r\n", cursor is on row 5 col 0.

	// View height >= total rows: cursor row is unchanged.
	r, c := s.CursorInView(100)
	if r != 5 || c != 0 {
		t.Fatalf("CursorInView(100) = (%d,%d), want (5,0)", r, c)
	}

	// View height 3: total rows=6, top=3, cursor(5) is visible at index 5-3=2.
	r, c = s.CursorInView(3)
	if r != 2 || c != 0 {
		t.Fatalf("CursorInView(3) = (%d,%d), want (2,0)", r, c)
	}
}

// TestCursorInView_AboveWindow verifies the cursor below the visible window
// returns (-1,-1) so the renderer skips drawing it.
func TestCursorInView_AboveWindow(t *testing.T) {
	s := NewScreen(40)
	p := NewParser(s)
	// Cursor at row 0; build more rows so row 0 scrolls out of a small view.
	p.Write([]byte("head\r\n")) // row 0 then cursor on row 1
	for i := 0; i < 5; i++ {
		p.Write([]byte("x\r\n"))
	}
	// Cursor ended past row 6; force it back to row 0 to simulate a cursor in
	// old scrollback while the view shows the bottom.
	s.SetCursor(0, 0)
	// total rows now ~7; view height 3 shows rows 4..6, cursor at row 0 is off.
	r, c := s.CursorInView(3)
	if r != -1 || c != -1 {
		t.Fatalf("CursorInView(3) with cursor at row 0 = (%d,%d), want (-1,-1)", r, c)
	}
}
