package sshterm

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
}

// Cell is one screen position: a rune plus SGR style bits.
type Cell struct {
	Ch    rune
	Style Style
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

// View returns the bottom `height` rows as a ready-to-render snapshot. If
// there are fewer rows than height, the leading rows are empty.
func (s *Screen) View(height int) [][]Cell {
	total := len(s.rows)
	if height >= total {
		return s.rows
	}
	return s.rows[total-height:]
}

// Cursor returns the current (row, col) cursor position.
func (s *Screen) Cursor() (int, int) { return s.curRow, s.curCol }

// CursorInView returns the cursor's (row, col) translated into the coordinate
// space of View(height) — i.e. row is the index within the bottom `height`
// rows, col is unchanged. It returns (-1, -1) when the cursor is above the
// visible window (in scrolled-back content). Used by the renderer to draw the
// cursor block on the right cell.
func (s *Screen) CursorInView(height int) (int, int) {
	total := len(s.rows)
	if height >= total {
		return s.curRow, s.curCol
	}
	top := total - height
	if s.curRow < top {
		return -1, -1
	}
	return s.curRow - top, s.curCol
}

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
