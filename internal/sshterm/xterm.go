package sshterm

import "sync/atomic"

// Screen is a minimal character grid that emulates the subset of xterm
// behavior needed for interactive shells: writing printable text, line
// wrapping, cursor movement, carriage return, newline, backspace, and the
// common CSI erase/cursor sequences. It is deliberately not a full terminal
// emulator — it targets shells, vim, top, htop rather than every escape code.
//
// The grid is rows x cols of Cells. Rows grow as needed; the model keeps an
// in-memory scrollback by appending rows rather than discarding them.
type Screen struct {
	cols int
	rows [][]Cell

	// cursor position; row may exceed len(rows)-1 conceptually but rendering
	// only shows the bottom `height` rows.
	curRow int
	curCol int

	// autowrap tracking: when the cursor is placed one past the last column by
	// a write, the next printable char wraps. We model the "pending wrap" flag.
	pendingWrap bool

	// scrollOff is how many rows above the live bottom edge the view is
	// scrolled back: 0 = anchored to live output, n = show n rows further up
	// into scrollback. Reset to 0 by ResetScroll / ScrollReset on new output.
	// The UI sets this via Scroll when the user reviews history.
	scrollOff int

	// viewH is the height last passed to View/CursorInView. ScrollTo uses it as
	// the clamp ceiling so the offset can't drift into a "dead zone" above the
	// topmost reachable row. 0 until the first render; ScrollTo falls back to a
	// looser ceiling in that case.
	viewH int

	outputVersion atomic.Uint64
}

// Cell is one screen position: a rune plus SGR style bits.
type Cell struct {
	Ch    rune
	Style Style
}

// Point is a row/column coordinate in the currently visible terminal window.
type Point struct {
	Row int
	Col int
}

// Style holds foreground/background color codes (16-color ANSI for MVP) plus
// the bold/dim/italic/underline/reverse attributes. Zero value = default.
type Style struct {
	Fg     int8 // -1 = default
	Bg     int8
	Bold   bool
	Dim    bool
	Italic bool
	Under  bool
	Rev    bool
}

// NewScreen creates a screen cols wide (height is dynamic; it exposes the
// bottom-most `h` rows for rendering).
func NewScreen(cols int) *Screen {
	if cols < 1 {
		cols = 1
	}
	s := &Screen{cols: cols}
	s.rows = [][]Cell{make([]Cell, cols)}
	return s
}

// Cols returns the configured width.
func (s *Screen) Cols() int { return s.cols }

// Rows returns the total number of rows in scrollback (may exceed height).
func (s *Screen) Rows() int { return len(s.rows) }

// View returns the `height` rows currently in view as a ready-to-render
// snapshot. With no scroll offset it shows the bottom (live) `height` rows;
// with a positive scroll offset (set by Scroll) the window moves up into the
// scrollback. If there are fewer rows than height, the leading rows are empty.
//
// View also remembers the rendering height as the clamp ceiling for the scroll
// offset (see ScrollTo), so the offset can never accumulate in a "dead zone"
// past the topmost reachable row.
func (s *Screen) View(height int) [][]Cell {
	s.viewH = height
	total := len(s.rows)
	if height >= total {
		return s.rows
	}
	// Live (offset 0): bottom-anchored window rows[total-height:].
	// Scrolled back (offset n): shift the window up by n -> rows[total-height-n : total-n].
	end := total - s.scrollOff
	if end < height {
		end = height
	}
	start := end - height
	return s.rows[start:end]
}

// TextRange extracts plain text from the current visible window. Coordinates
// are inclusive and are clamped to the visible window.
func (s *Screen) TextRange(height int, start, end Point) string {
	if height < 1 || s.cols < 1 {
		return ""
	}
	view := s.View(height)
	if len(view) == 0 {
		return ""
	}
	start = clampPoint(start, height, s.cols)
	end = clampPoint(end, height, s.cols)
	if pointAfter(start, end) {
		start, end = end, start
	}
	var out []rune
	for r := start.Row; r <= end.Row; r++ {
		var row []Cell
		if r >= 0 && r < len(view) {
			row = view[r]
		}
		from, to := 0, s.cols-1
		if r == start.Row {
			from = start.Col
		}
		if r == end.Row {
			to = end.Col
		}
		if from < 0 {
			from = 0
		}
		if to >= s.cols {
			to = s.cols - 1
		}
		line := cellsText(row, from, to)
		out = append(out, line...)
		if r != end.Row {
			out = append(out, '\n')
		}
	}
	return string(out)
}

func cellsText(row []Cell, from, to int) []rune {
	if to < from {
		return nil
	}
	line := make([]rune, 0, to-from+1)
	for c := from; c <= to; c++ {
		ch := rune(0)
		if c < len(row) {
			ch = row[c].Ch
		}
		if ch == 0 {
			ch = ' '
		}
		line = append(line, ch)
	}
	for len(line) > 0 && line[len(line)-1] == ' ' {
		line = line[:len(line)-1]
	}
	return line
}

func clampPoint(p Point, rows, cols int) Point {
	if p.Row < 0 {
		p.Row = 0
	}
	if p.Row >= rows {
		p.Row = rows - 1
	}
	if p.Col < 0 {
		p.Col = 0
	}
	if p.Col >= cols {
		p.Col = cols - 1
	}
	return p
}

func pointAfter(a, b Point) bool {
	if a.Row != b.Row {
		return a.Row > b.Row
	}
	return a.Col > b.Col
}

// Cursor returns the current (row, col) cursor position.
func (s *Screen) Cursor() (int, int) { return s.curRow, s.curCol }

// CursorInView returns the cursor's (row, col) translated into the coordinate
// space of View(height) — i.e. row is the index within the visible window, col
// is unchanged. It returns (-1, -1) when the cursor is outside the visible
// window: either scrolled back above it, or below it. Used by the renderer to
// draw the cursor block on the right cell.
func (s *Screen) CursorInView(height int) (int, int) {
	s.viewH = height
	total := len(s.rows)
	if height >= total {
		return s.curRow, s.curCol
	}
	// The visible window covers absolute rows [start, end).
	end := total - s.scrollOff
	start := end - height
	if s.curRow < start || s.curRow >= end {
		return -1, -1
	}
	return s.curRow - start, s.curCol
}

// ScrollOffset returns how many rows above the live bottom the view is scrolled
// back (0 = live output).
func (s *Screen) ScrollOffset() int { return s.scrollOff }

// OutputVersion changes whenever the remote output stream appends more data.
// UI selections are anchored to visible coordinates, so callers can use this to
// invalidate selections before copying stale coordinates from a shifted view.
func (s *Screen) OutputVersion() uint64 { return s.outputVersion.Load() }

// MarkOutput records that remote output changed the backing screen.
func (s *Screen) MarkOutput() { s.outputVersion.Add(1) }

// Scroll moves the view by delta rows (positive = further up into scrollback,
// negative = back toward live output). The offset is clamped by ScrollTo to the
// reachable range so it can't accumulate past the topmost visible row.
func (s *Screen) Scroll(delta int) {
	s.ScrollTo(s.scrollOff + delta)
}

// ScrollTo sets the absolute scroll offset (rows above the live bottom). It is
// clamped to [0, total-viewH]: scrolling further than that would run the
// viewport past the top of the buffer, and clamping there (rather than at
// total-1) avoids a "dead zone" where offsets between total-viewH and total-1
// all render the same top-anchored view — without this, the user could scroll
// up into that invisible range and then had to scroll back down through it
// before the view moved again.
//
// viewH is the height last passed to View/CursorInView. If the screen has not
// been rendered yet (viewH == 0), we fall back to total-1 so ScrollTo still has
// a sane ceiling; the first render tightens it.
func (s *Screen) ScrollTo(off int) {
	max := s.scrollMax()
	switch {
	case off < 0:
		off = 0
	case off > max:
		off = max
	}
	s.scrollOff = off
}

// scrollMax is the largest meaningful offset given the known view height.
func (s *Screen) scrollMax() int {
	total := len(s.rows)
	if total < 1 {
		return 0
	}
	if s.viewH > 0 && total-s.viewH > 0 {
		return total - s.viewH
	}
	// No view height known yet, or the whole buffer fits: allow up to total-1
	// as a harmless ceiling; the next View() call tightens it.
	return total - 1
}

// ResetScroll re-anchors the view to live (bottom) output.
func (s *Screen) ResetScroll() { s.scrollOff = 0 }

// curColRow ensures curRow references a real row; grows scrollback as needed.
func (s *Screen) ensureRow(r int) {
	for len(s.rows) <= r {
		s.rows = append(s.rows, make([]Cell, s.cols))
	}
}

// Print writes a single rune at the cursor, honoring autowrap.
func (s *Screen) Print(ch rune, style Style) {
	if s.pendingWrap {
		s.curRow++
		s.curCol = 0
		s.pendingWrap = false
	}
	s.ensureRow(s.curRow)
	if s.curCol >= s.cols {
		// hard wrap if somehow past
		s.curRow++
		s.curCol = 0
		s.ensureRow(s.curRow)
	}
	s.rows[s.curRow][s.curCol] = Cell{Ch: ch, Style: style}
	s.curCol++
	if s.curCol >= s.cols {
		s.curCol = s.cols - 1
		s.pendingWrap = true
	}
}

// CarriageReturn moves the cursor to column 0 on the current row.
func (s *Screen) CarriageReturn() {
	s.curCol = 0
	s.pendingWrap = false
}

// LineFeed moves the cursor down one row (scrolling the grid by appending a
// blank row when at the bottom). It does NOT reset the column (that is what
// CR is for); real terminals combine \r\n.
func (s *Screen) LineFeed() {
	s.curRow++
	s.ensureRow(s.curRow)
	s.pendingWrap = false
}

// Backspace moves the cursor left one column (clamped at 0), clearing any
// pending wrap.
func (s *Screen) Backspace() {
	if s.pendingWrap {
		s.pendingWrap = false
		return
	}
	if s.curCol > 0 {
		s.curCol--
	}
}

// Tab moves the cursor to the next multiple of 8.
func (s *Screen) Tab() {
	next := ((s.curCol / 8) + 1) * 8
	if next >= s.cols {
		next = s.cols - 1
	}
	s.curCol = next
	s.pendingWrap = false
}

// SetCursor positions the cursor absolutely (0-based). Out-of-range values are
// clamped to the grid.
func (s *Screen) SetCursor(row, col int) {
	if row < 0 {
		row = 0
	}
	if col < 0 {
		col = 0
	}
	if col >= s.cols {
		col = s.cols - 1
	}
	s.curRow = row
	s.ensureRow(row)
	s.curCol = col
	s.pendingWrap = false
}

// MoveCursor applies a relative cursor move by dRow/dCol (clamped >= 0).
func (s *Screen) MoveCursor(dRow, dCol int) {
	s.SetCursor(s.curRow+dRow, s.curCol+dCol)
}

// ClearLine erases part of the current line. mode: 0=cursor to end,
// 1=start to cursor (inclusive), 2=entire line.
func (s *Screen) ClearLine(mode int) {
	s.ensureRow(s.curRow)
	row := s.rows[s.curRow]
	blank := Cell{}
	switch mode {
	case 0:
		for c := s.curCol; c < s.cols; c++ {
			row[c] = blank
		}
	case 1:
		for c := 0; c <= s.curCol && c < s.cols; c++ {
			row[c] = blank
		}
	case 2:
		for c := 0; c < s.cols; c++ {
			row[c] = blank
		}
	}
}

// DeleteChars deletes n chars at the cursor, shifting the rest of the line
// left and filling the freed cells at the right edge with blanks (CSI Pn P =
// DCH). This is what readline emits to remove a character mid-line. The cursor
// position is unchanged. n defaults to 1 and is clamped to the remaining width.
func (s *Screen) DeleteChars(n int) {
	if n < 1 {
		n = 1
	}
	s.ensureRow(s.curRow)
	row := s.rows[s.curRow]
	rem := s.cols - s.curCol
	if n > rem {
		n = rem
	}
	// shift left by n
	for c := s.curCol; c+n < s.cols; c++ {
		row[c] = row[c+n]
	}
	// blank the freed tail
	for c := s.cols - n; c < s.cols; c++ {
		row[c] = Cell{}
	}
}

// InsertChars inserts n blank chars at the cursor, shifting the existing chars
// to the right (chars past the right edge are lost). CSI Pn @ = ICH. Used by
// readline when inserting a typed character mid-line.
func (s *Screen) InsertChars(n int) {
	if n < 1 {
		n = 1
	}
	s.ensureRow(s.curRow)
	row := s.rows[s.curRow]
	rem := s.cols - s.curCol
	if n > rem {
		n = rem
	}
	// shift right by n
	for c := s.cols - 1; c-n >= s.curCol; c-- {
		row[c] = row[c-n]
	}
	// blank the inserted gap
	for c := s.curCol; c < s.curCol+n && c < s.cols; c++ {
		row[c] = Cell{}
	}
}

// EraseChars erases n chars at and after the cursor, replacing them with blanks
// without moving anything (CSI Pn X = ECH). Cursor is unchanged.
func (s *Screen) EraseChars(n int) {
	if n < 1 {
		n = 1
	}
	s.ensureRow(s.curRow)
	row := s.rows[s.curRow]
	end := s.curCol + n
	if end > s.cols {
		end = s.cols
	}
	for c := s.curCol; c < end; c++ {
		row[c] = Cell{}
	}
}

// ClearScreen erases part of the screen. mode: 0=cursor to end of screen,
// 1=start of screen to cursor, 2=entire screen, 3=entire screen + scrollback.
// The cursor is not moved (clear sends CSI H to home separately).
//
// For mode 2/3 we reset the grid to a single blank row so the View window,
// which shows the bottom `height` rows, re-anchors at the cursor instead of
// staying pinned to stale (now-blank) scrollback. Without this, `clear` left
// curRow=0 writing into row 0 while View() returned empty rows from the old
// bottom — making the screen look blank until enough new output scrolled the
// cursor back into the visible window.
func (s *Screen) ClearScreen(mode int) {
	blank := Cell{}
	switch mode {
	case 0:
		s.ensureRow(s.curRow)
		// erase rest of current line
		for c := s.curCol; c < s.cols; c++ {
			s.rows[s.curRow][c] = blank
		}
		for r := s.curRow + 1; r < len(s.rows); r++ {
			for c := 0; c < s.cols; c++ {
				s.rows[r][c] = blank
			}
		}
	case 1:
		s.ensureRow(s.curRow)
		for r := 0; r < s.curRow; r++ {
			for c := 0; c < s.cols; c++ {
				s.rows[r][c] = blank
			}
		}
		for c := 0; c <= s.curCol && c < s.cols; c++ {
			s.rows[s.curRow][c] = blank
		}
	case 2, 3:
		// Truncate scrollback entirely and keep a single blank row at the
		// cursor's row (clear normally homes the cursor first via CSI H).
		keep := s.curRow
		if keep < 0 {
			keep = 0
		}
		s.rows = s.rows[:0]
		for r := 0; r <= keep; r++ {
			s.rows = append(s.rows, make([]Cell, s.cols))
		}
	}
}
