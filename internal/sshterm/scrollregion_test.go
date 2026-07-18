package sshterm

import (
	"slices"
	"testing"
)

// TestScrollRegion_LineFeedScrollsRegion reproduces the vim long-file bug.
// vim sets a DECSTBM scroll region that leaves the last row for its status
// line (CSI 1 ; H-1 r). When the cursor reaches the region bottom, LineFeed
// must scroll only the region [top, bottom] up — the status line below the
// region stays put, instead of the whole viewport scrolling or the cursor
// running past the region.
func TestScrollRegion_LineFeedScrollsRegion(t *testing.T) {
	const h = 6
	s := NewScreen(12)
	s.SetHeight(h)
	p := NewParser(s)

	// Enter alt screen, set a scroll region [0, h-2] (leave row h-1 as status).
	p.Write([]byte("\x1b[?1049h"))
	p.Write([]byte("\x1b[1;5r")) // region rows 1..5 (0-based 0..4)

	// Paint the status line on row h-1 (outside the region) and home the cursor.
	p.Write([]byte("\x1b[6;1HSTATUS\x1b[1;1H"))

	// Fill the region: 5 content lines into a 5-row region. Writing line 5
	// ends at the region bottom; a further LineFeed must scroll the region up.
	p.Write([]byte("L1\r\nL2\r\nL3\r\nL4\r\nL5"))

	// Cursor should be at the region bottom (row 4), not past it.
	if cr, _ := s.Cursor(); cr != 4 {
		t.Fatalf("cursor row = %d, want 4 (region bottom)", cr)
	}

	// Another LineFeed at the region bottom: region scrolls up by one.
	// L1 scrolls off, L2..L5 move up, row 4 blanks. STATUS (row 5) stays.
	p.Write([]byte("\r\n"))
	if cr, _ := s.Cursor(); cr != 4 {
		t.Fatalf("cursor row after region scroll = %d, want 4 (stays at bottom)", cr)
	}

	if got, want := screenLines(s), []string{"L2", "L3", "L4", "L5", "", "STATUS"}; !slices.Equal(got, want) {
		t.Fatalf("lines = %v, want %v", got, want)
	}
}

// TestScrollRegion_ResetOnEnterAlt verifies that entering the alt screen
// clears any prior region, so a fresh vim session starts with a full viewport.
func TestScrollRegion_ResetOnEnterAlt(t *testing.T) {
	const h = 8
	s := NewScreen(10)
	s.SetHeight(h)
	p := NewParser(s)
	p.Write([]byte("\x1b[?1049h"))
	p.Write([]byte("\x1b[2;6r")) // a narrow region
	// Re-enter alt screen: region should reset to full viewport.
	p.Write([]byte("\x1b[?1049h\x1b[H"))
	p.Write([]byte("L1\r\nL2\r\nL3\r\nL4\r\nL5\r\nL6\r\nL7\r\nL8\r\nL9"))

	// With a full viewport, 9 lines into 8 rows scrolls to L2..L9.
	if cr, _ := s.Cursor(); cr != h-1 {
		t.Fatalf("cursor = %d, want %d (full-viewport bottom)", cr, h-1)
	}
	if got := screenLines(s)[0]; got != "L2" {
		t.Errorf("row 0 = %q, want L2 (region was reset on alt enter)", got)
	}
}
