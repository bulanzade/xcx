package sshterm

import (
	"strings"
	"testing"
)

// fillLines prints each line followed by \r\n, producing a scrollback where
// total rows == len(lines)+1 (the final LineFeed appends one trailing blank
// row, exactly like a shell prompt that has emitted a newline). It returns the
// resulting row count so tests can build precise expectations.
func fillLines(s *Screen, lines string) int {
	for _, ln := range strings.Split(lines, "|") {
		for _, r := range ln {
			s.Print(r, Style{})
		}
		s.CarriageReturn()
		s.LineFeed()
	}
	return s.Rows()
}

// TestScroll_ViewBottomByDefault verifies that with no scroll offset the view
// is anchored to the bottom (live output) — the pre-existing behavior that
// scroll support must preserve.
func TestScroll_ViewBottomByDefault(t *testing.T) {
	s := NewScreen(3)
	total := fillLines(s, "AAA|BBB|CCC|DDD|EEE") // 6 rows incl. trailing blank
	// Live view shows the bottom 2 rows: EEE, then the trailing blank.
	view := s.View(2)
	if got := len(view); got != 2 {
		t.Fatalf("View(2) len = %d, want 2", got)
	}
	if rowText(view[0]) != "EEE" {
		t.Errorf("View(2)[0] = %q, want EEE (rows=%d)", rowText(view[0]), total)
	}
}

// TestScroll_ScrollBack checks that Scroll moves the visible window up into the
// scrollback and that View reflects it.
func TestScroll_ScrollBack(t *testing.T) {
	s := NewScreen(3)
	fillLines(s, "AAA|BBB|CCC|DDD|EEE") // 6 rows: AAA,BBB,CCC,DDD,EEE,blank
	s.Scroll(1)                         // up one line from the bottom
	view := s.View(2)
	if rowText(view[0]) != "DDD" || rowText(view[1]) != "EEE" {
		t.Errorf("after Scroll(1) View(2) = %q,%q, want DDD,EEE", rowText(view[0]), rowText(view[1]))
	}
	if off := s.ScrollOffset(); off != 1 {
		t.Errorf("ScrollOffset = %d, want 1", off)
	}
	s.Scroll(1) // up another
	view = s.View(2)
	if rowText(view[0]) != "CCC" || rowText(view[1]) != "DDD" {
		t.Errorf("after Scroll(2) View(2) = %q,%q, want CCC,DDD", rowText(view[0]), rowText(view[1]))
	}
}

// TestScroll_ScrollThenDown checks scrolling back up then back down returns to
// live output.
func TestScroll_ScrollThenDown(t *testing.T) {
	s := NewScreen(3)
	fillLines(s, "AAA|BBB|CCC|DDD|EEE")
	s.Scroll(3)  // up 3
	s.Scroll(-2) // down 2 -> offset 1
	view := s.View(2)
	if rowText(view[0]) != "DDD" || rowText(view[1]) != "EEE" {
		t.Errorf("after up3 down2 View(2) = %q,%q, want DDD,EEE", rowText(view[0]), rowText(view[1]))
	}
}

// TestScroll_Clamp ensures the offset can't go negative or past the top.
func TestScroll_Clamp(t *testing.T) {
	s := NewScreen(3)
	fillLines(s, "AAA|BBB|CCC") // 4 rows: AAA,BBB,CCC,blank
	s.Scroll(-5)                // can't go below 0
	if off := s.ScrollOffset(); off != 0 {
		t.Errorf("Scroll(-5) offset = %d, want 0", off)
	}
	s.Scroll(100) // can't exceed total-1
	if off := s.ScrollOffset(); off != 3 {
		t.Errorf("Scroll(100) offset = %d, want 3", off)
	}
	// At max offset with height 2, View shows the topmost rows (AAA, BBB).
	view := s.View(2)
	if rowText(view[0]) != "AAA" || rowText(view[1]) != "BBB" {
		t.Errorf("max scroll View(2) = %q,%q, want AAA,BBB", rowText(view[0]), rowText(view[1]))
	}
}

// TestScroll_ResetScroll zeroes the offset (back to live output).
func TestScroll_ResetScroll(t *testing.T) {
	s := NewScreen(3)
	fillLines(s, "AAA|BBB")
	s.Scroll(2)
	if s.ScrollOffset() != 2 {
		t.Fatalf("offset before reset = %d, want 2", s.ScrollOffset())
	}
	s.ResetScroll()
	if s.ScrollOffset() != 0 {
		t.Errorf("after ResetScroll offset = %d, want 0", s.ScrollOffset())
	}
}

func TestScreenOutputVersion(t *testing.T) {
	s := NewScreen(3)
	if got := s.OutputVersion(); got != 0 {
		t.Fatalf("initial output version = %d, want 0", got)
	}
	s.MarkOutput()
	if got := s.OutputVersion(); got != 1 {
		t.Fatalf("after MarkOutput version = %d, want 1", got)
	}
}

// TestScroll_NoDeadZoneAtTop reproduces the bug where scrolling past the top of
// scrollback built up offset in a "dead zone" (offsets between total-height and
// total-1 all render the same top-anchored view), so the user had to scroll
// back down through that wasted range before the view moved. The fix clamps the
// offset to total-height: once at the top, further up-scrolls are no-ops and an
// immediate down-scroll moves the view right away.
//
// Scenario: buffer of 10 rows, view height 4. Max meaningful offset = 6
// (window then covers rows 0..3, the very top). Scrolling up 100 must clamp to
// exactly 6, not higher.
func TestScroll_NoDeadZoneAtTop(t *testing.T) {
	s := NewScreen(3)
	total := fillLines(s, "AAA|BBB|CCC|DDD|EEE|FFF|GGG|HHH|III") // 10 rows
	const h = 4
	maxOff := total - h // 6
	if maxOff != 6 {
		t.Fatalf("maxOff = %d, want 6 (total=%d)", maxOff, total)
	}
	// Seed the terminal height so the scroll clamp is tight (View no longer
	// seeds it; a real terminal sets it via resize before any scroll arrives).
	s.SetHeight(h)

	// Scroll way past the top.
	s.Scroll(100)
	if off := s.ScrollOffset(); off != maxOff {
		t.Errorf("over-scroll offset = %d, want %d (no dead zone accumulation)", off, maxOff)
	}
	// At max offset, the view shows the topmost 4 rows.
	view := s.View(h)
	if rowText(view[0]) != "AAA" || rowText(view[3]) != "DDD" {
		t.Errorf("max-offset view = %q..%q, want AAA..DDD", rowText(view[0]), rowText(view[3]))
	}
	// ONE down-scroll must immediately move the view down by one (no dead zone
	// to burn through first).
	s.Scroll(-1)
	if off := s.ScrollOffset(); off != maxOff-1 {
		t.Errorf("after 1 down-scroll offset = %d, want %d", off, maxOff-1)
	}
	view = s.View(h)
	if rowText(view[0]) != "BBB" {
		t.Errorf("after 1 down-scroll view[0] = %q, want BBB (should move immediately)", rowText(view[0]))
	}
}

// (-1,-1) when the user has scrolled away from the live cursor — the curso
// (-1,-1) when the user has scrolled away from the live cursor — the cursor
// is in live (bottom-anchored) content and shouldn't be drawn in scrollback.
func TestScroll_CursorHiddenWhenScrolledBack(t *testing.T) {
	s := NewScreen(3)
	fillLines(s, "AAA|BBB|CCC|DDD|EEE")
	// cursor sits in the live region; scrolling up should hide it.
	s.Scroll(1)
	r, c := s.CursorInView(2)
	if r != -1 || c != -1 {
		t.Errorf("CursorInView when scrolled back = (%d,%d), want (-1,-1)", r, c)
	}
	// back to live: cursor is visible again.
	s.ResetScroll()
	r, c = s.CursorInView(2)
	if r == -1 || c == -1 {
		t.Errorf("CursorInView when live = (%d,%d), want visible", r, c)
	}
}
