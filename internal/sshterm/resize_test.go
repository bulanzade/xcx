package sshterm

import (
	"strings"
	"testing"
)

// TestScreen_SetWidth verifies the grid's row storage follows width changes:
// rows are padded when widening, and narrowing keeps the stored cells —
// clipped at display — instead of truncating them. Printing afterwards must
// use the new width without panicking on rows allocated at the old one.
func TestScreen_SetWidth(t *testing.T) {
	s := NewScreen(10)
	s.SetHeight(4)
	p := NewParser(s)
	p.Write([]byte("0123456789"))

	// Widen: old row padded, next full-width line must not wrap.
	s.SetWidth(12)
	p.Write([]byte("\r\nABCDEFGHIJKL"))
	rows := screenRows(s)
	if len(rows) < 2 || len(rows[1]) != 12 || rowText(rows[1]) != "ABCDEFGHIJKL" {
		t.Fatalf("row after widen = %v, want one 12-cell row ABCDEFGHIJKL", rowsText(rows))
	}

	// Narrow: printing uses the new width…
	s.SetWidth(6)
	p.Write([]byte("\r\nabcdef"))
	rows = screenRows(s)
	last := rows[len(rows)-1]
	if len(last) != 6 || rowText(last) != "abcdef" {
		t.Fatalf("row after narrow = %q (%d cells), want abcdef (6)", rowText(last), len(last))
	}
	// …the earlier wide row is clipped to the display width…
	if got := rowText(rows[0]); got != "012345" {
		t.Fatalf("old row display after narrow = %q, want clipped 012345", got)
	}
	// …but its cells stay in storage, so a re-widen can restore them.
	if n := storedRowLen(s, 0); n != 12 {
		t.Fatalf("old row storage after narrow = %d cells, want 12 preserved", n)
	}
}

// storedRowLen returns the raw stored length of a row, bypassing View's
// display clipping: the preserve-tails resize contract is about storage.
func storedRowLen(s *Screen, r int) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if r >= len(s.active.rows) {
		return -1
	}
	return len(s.active.rows[r])
}

// TestScreen_NarrowPreservesHistory pins the narrowing resize semantics:
// cells beyond the new width must not be deleted from history. They stay in
// storage (clipped at display), so re-widening restores the full line;
// rewriting or erasing a line at the narrower width drops its stale tail
// instead of letting it concatenate with the rewrite on re-widen.
func TestScreen_NarrowPreservesHistory(t *testing.T) {
	s := NewScreen(10)
	s.SetHeight(4)
	p := NewParser(s)
	p.Write([]byte("0123456789"))

	s.SetWidth(5)
	if got := rowText(screenRows(s)[0]); got != "01234" {
		t.Fatalf("display after narrow = %q, want clipped 01234", got)
	}
	if n := storedRowLen(s, 0); n != 10 {
		t.Fatalf("storage after narrow = %d cells, want 10 preserved", n)
	}
	s.SetWidth(10)
	if got := rowText(screenRows(s)[0]); got != "0123456789" {
		t.Fatalf("row after re-widen = %q, want restored 0123456789", got)
	}

	// Rewriting the line at the narrower width drops the stale tail: the
	// re-widened row shows the rewrite, not "X" glued to the old "56789".
	s.SetWidth(5)
	p.Write([]byte("\rX"))
	s.SetWidth(10)
	if got := rowText(screenRows(s)[0]); got != "X1234" {
		t.Fatalf("rewritten row after re-widen = %q, want X1234 (tail dropped)", got)
	}

	// Erasing the line (EL 2) covers the preserved tail too.
	s.SetWidth(5)
	p.Write([]byte("\r\x1b[2Kfull"))
	s.SetWidth(10)
	if got := rowText(screenRows(s)[0]); got != "full" {
		t.Fatalf("erased row after re-widen = %q, want full", got)
	}
}

// TestScreen_WidenPendingWrapAdvancesCursor pins the pendingWrap resize rule:
// a full row at the old width deferred its wrap; widening must move the
// insertion column past the last cell instead of leaving the cursor on it,
// or the next printed rune overwrote the row's last character (abcde, widen,
// then f came out as "abcdf").
func TestScreen_WidenPendingWrapAdvancesCursor(t *testing.T) {
	s := NewScreen(5)
	s.SetHeight(4)
	p := NewParser(s)
	p.Write([]byte("abcde")) // fills the row; wrap deferred
	s.SetWidth(10)
	p.Write([]byte("f"))
	rows := screenRows(s)
	if got := rowText(rows[0]); got != "abcdef" {
		t.Fatalf("row after widen+print = %q, want abcdef", got)
	}
	if len(rows) > 1 && rowText(rows[1]) != "" {
		t.Fatalf("print wrapped onto a second row: %q", rowsText(rows))
	}
}

// TestScreen_AltRebuildClampsCursor pins the panic guard: shrinking the
// height while the alt screen holds the cursor on a tall row must not leave
// curRow past the new bound for the next write.
func TestScreen_AltRebuildClampsCursor(t *testing.T) {
	s := NewScreen(20)
	s.SetHeight(24)
	p := NewParser(s)
	p.Write([]byte("\x1b[?1049h\x1b[20;5HX"))
	if r, _ := s.Cursor(); r != 19 {
		t.Fatalf("setup: cursor row = %d, want 19", r)
	}
	s.SetHeight(8)       // shrink below the cursor's row
	p.Write([]byte("Y")) // must not panic
	if r, _ := s.Cursor(); r > 7 {
		t.Fatalf("cursor row after shrink = %d, want <= 7", r)
	}
}

func rowsText(rows [][]Cell) string {
	var b strings.Builder
	for i, r := range rows {
		if i > 0 {
			b.WriteString("|")
		}
		b.WriteString(rowText(r))
	}
	return b.String()
}

// TestScreen_WidthResizeDoesNotWrapRedraw reproduces the pi-agent bug: the
// remote TUI (differential renderer) redraws its full-width input box after
// SIGWINCH using \r + EL2 + the new-width line. If the grid kept the stale
// width, that line wrapped mid-box and only the left part of the box showed.
func TestScreen_WidthResizeDoesNotWrapRedraw(t *testing.T) {
	s := NewScreen(20)
	s.SetHeight(5)
	p := NewParser(s)
	// Box border at the original width.
	p.Write([]byte("╭──────────────────╮"))
	// Window grows; remote redraws the border at the new width.
	s.SetWidth(30)
	p.Write([]byte("\r\x1b[2K╭────────────────────────────╮"))

	rows := screenRows(s)
	want := "╭" + strings.Repeat("─", 28) + "╮"
	if got := rowText(rows[0]); got != want {
		t.Fatalf("redraw after widen = %q, want full-width border %q", got, want)
	}
	if len(rows) > 1 && rowText(rows[1]) != "" {
		t.Fatalf("border wrapped onto a second row: %q", rowsText(rows))
	}
}

// TestTerminal_ResizeSyncsScreenWidth is the terminal-level regression test:
// Resize must update the screen's wrap width along with the PTY size, so
// programs redrawing at the new width don't wrap against the stale grid.
func TestTerminal_ResizeSyncsScreenWidth(t *testing.T) {
	fake := newFakePty("")
	term, err := NewTerminal(fake, 80, 24)
	if err != nil {
		t.Fatalf("NewTerminal: %v", err)
	}
	defer term.Close()
	if err := term.Resize(120, 40); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if fake.changedW != 120 {
		t.Fatalf("PTY width after resize = %d, want 120", fake.changedW)
	}
	if cols := term.Screen().Cols(); cols != 120 {
		t.Fatalf("screen cols after resize = %d, want 120", cols)
	}
	// A full-width line at the new PTY width must stay on one grid row.
	term.FeedOutput([]byte(strings.Repeat("x", 120)))
	rows := screenRows(term.Screen())
	if len(rows) != 1 || len(rows[0]) != 120 {
		t.Fatalf("line wrapped at stale width: %d rows", len(rows))
	}
}
