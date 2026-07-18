package sshterm

import (
	"slices"
	"testing"
)

// TestInsertLines shifts the cursor row and below down within the region,
// dropping rows past the region bottom. CSI L is what vim emits when scrolling
// text into view from above.
func TestInsertLines(t *testing.T) {
	const h = 5
	s := NewScreen(6)
	s.SetHeight(h)
	p := NewParser(s)
	p.Write([]byte("\x1b[?1049h\x1b[H"))
	p.Write([]byte("A\r\nB\r\nC\r\nD\r\nE")) // rows 0..4

	// Move to row 1, insert 2 lines: B,C shift down; rows 1,2 blank; D,E lost.
	p.Write([]byte("\x1b[2;1H\x1b[2L"))
	if got, want := screenLines(s), []string{"A", "", "", "B", "C"}; !slices.Equal(got, want) {
		t.Fatalf("lines = %v, want %v", got, want)
	}
}

// TestDeleteLines removes rows at the cursor and shifts the rest up, blanking
// the region bottom. CSI M is what vim emits when scrolling text into view
// from below.
func TestDeleteLines(t *testing.T) {
	const h = 5
	s := NewScreen(6)
	s.SetHeight(h)
	p := NewParser(s)
	p.Write([]byte("\x1b[?1049h\x1b[H"))
	p.Write([]byte("A\r\nB\r\nC\r\nD\r\nE"))

	// At row 1, delete 2 lines: B,C removed; D,E shift up; rows 3,4 blank.
	p.Write([]byte("\x1b[2;1H\x1b[2M"))
	if got, want := screenLines(s), []string{"A", "D", "E", "", ""}; !slices.Equal(got, want) {
		t.Fatalf("lines = %v, want %v", got, want)
	}
}

// TestScrollRegionUp scrolls the whole region up by n, blanking the bottom.
// CSI S scrolls regardless of cursor position within the region.
func TestScrollRegionUp(t *testing.T) {
	const h = 5
	s := NewScreen(6)
	s.SetHeight(h)
	p := NewParser(s)
	p.Write([]byte("\x1b[?1049h\x1b[H"))
	p.Write([]byte("A\r\nB\r\nC\r\nD\r\nE"))

	p.Write([]byte("\x1b[2S")) // scroll up 2: A,B off; C,D,E up; rows 3,4 blank
	if got, want := screenLines(s), []string{"C", "D", "E", "", ""}; !slices.Equal(got, want) {
		t.Fatalf("lines = %v, want %v", got, want)
	}
}

// TestScrollRegionDown scrolls the whole region down by n, blanking the top.
// CSI T is the reverse scroll vim uses when moving the view up.
func TestScrollRegionDown(t *testing.T) {
	const h = 5
	s := NewScreen(6)
	s.SetHeight(h)
	p := NewParser(s)
	p.Write([]byte("\x1b[?1049h\x1b[H"))
	p.Write([]byte("A\r\nB\r\nC\r\nD\r\nE"))

	p.Write([]byte("\x1b[2T")) // scroll down 2: D,E off; A,B,C down; rows 0,1 blank
	if got, want := screenLines(s), []string{"", "", "A", "B", "C"}; !slices.Equal(got, want) {
		t.Fatalf("lines = %v, want %v", got, want)
	}
}

// TestScrollLinesRespectRegion verifies IL/DL/SU/SD stay within the DECSTBM
// region: rows outside the region (vim's status line) must not move.
func TestScrollLinesRespectRegion(t *testing.T) {
	const h = 6
	s := NewScreen(10)
	s.SetHeight(h)
	p := NewParser(s)
	p.Write([]byte("\x1b[?1049h"))
	p.Write([]byte("\x1b[1;5r")) // region rows 1..5 (0-based 0..4)
	p.Write([]byte("\x1b[6;1HSTATUS\x1b[1;1H"))
	p.Write([]byte("A\r\nB\r\nC\r\nD\r\nE"))

	// Scroll up 1 (SU) within region: A off, B..E up, row 4 blank; STATUS stays.
	p.Write([]byte("\x1b[1S"))
	if got, want := screenLines(s), []string{"B", "C", "D", "E", "", "STATUS"}; !slices.Equal(got, want) {
		t.Fatalf("lines = %v, want %v", got, want)
	}
}
