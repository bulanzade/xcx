package sshterm

import (
	"strings"
	"sync"
	"sync/atomic"
)

// Screen is a minimal character grid that emulates the subset of xterm
// behavior needed for interactive shells: writing printable text, line
// wrapping, cursor movement, carriage return, newline, backspace, and the
// common CSI erase/cursor sequences. It is deliberately not a full terminal
// emulator — it targets shells, vim, top, htop rather than every escape code.
//
// Screen holds two buffers and routes every operation to the active one:
//
//   - main: an unbounded grid that grows as output arrives and keeps full
//     scrollback. The view is bottom-anchored so the latest output shows.
//   - alt: a fixed-height grid (the alternate screen, entered via
//     ESC [ ? 1049 h) used by full-screen programs like vim/less/top. It
//     scrolls within its viewport instead of growing, and the view is
//     top-anchored so content written at row 0 stays visible.
//
// Without the alt buffer, vim rendered into the unbounded main buffer: its ~
// placeholder lines and status row grew total far past the view height, and
// the bottom-anchored view showed vim's bottom rows instead of the file
// content written at the top.
type Screen struct {
	cols int

	main   *grid
	alt    *grid
	active *grid

	// mu guards the active pointer and all grid state (rows/cursor/scrollOff/
	// viewH/height). The read loop writes (Print/LineFeed/EnterAltScreen/...)
	// while the UI goroutine reads (View/CursorInView/...) and resizes
	// (SetHeight). Without it, resizing the window while output arrives indexes
	// a grid whose rows are being reallocated — an out-of-bounds crash, not
	// just a stale read.
	mu sync.RWMutex

	// currentDir is the remote shell working directory most recently reported
	// through terminal integration (OSC 7 and compatible sequences). It is
	// atomic because the parser updates it in the PTY read loop while the UI
	// reads it when opening SFTP. It is shared across both buffers.
	currentDir atomic.Pointer[string]

	outputVersion atomic.Uint64
}

// grid is one screen buffer: a row array plus cursor and view state. Whether
// it grows unbounded (main) or is capped at a fixed height (alt) is controlled
// by fixed + height.
type grid struct {
	cols int
	rows [][]Cell

	// cursor position.
	curRow int
	curCol int

	// autowrap tracking: when the cursor is placed one past the last column by
	// a write, the next printable char wraps.
	pendingWrap bool

	// scrollOff is how many rows above the live bottom the view is scrolled
	// back (main buffer only). 0 = live output.
	scrollOff int

	// viewH is the height last passed to View/CursorInView. For the main buffer
	// it's the scroll clamp ceiling; for the alt buffer it's the fixed height.
	viewH int

	// fixed, when true, makes this grid behave as the alternate screen:
	// height-capped rows, in-viewport scroll on LineFeed at the bottom, and a
	// top-anchored View.
	fixed  bool
	height int // only meaningful when fixed

	// scrollTop/scrollBottom are the DECSTBM scroll region boundaries
	// (0-based, inclusive). -1 means "not set / full viewport". When the cursor
	// reaches scrollBottom, advanceRow scrolls [scrollTop, scrollBottom] up by
	// one instead of moving the cursor, so vim/less status lines outside the
	// region stay put.
	scrollTop, scrollBottom int
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
	s.main = newGrid(cols, false, 0)
	s.alt = newGrid(cols, true, 1) // height filled in on EnterAltScreen/resize
	s.active = s.main
	return s
}

func newGrid(cols int, fixed bool, height int) *grid {
	if height < 1 {
		height = 1
	}
	g := &grid{cols: cols, fixed: fixed, height: height, scrollTop: -1, scrollBottom: -1}
	if fixed {
		g.rows = make([][]Cell, height)
		for i := range g.rows {
			g.rows[i] = make([]Cell, cols)
		}
	} else {
		g.rows = [][]Cell{make([]Cell, cols)}
	}
	return g
}

// Cols returns the configured width. Width is immutable after construction,
// so no lock is needed.
func (s *Screen) Cols() int { return s.cols }

// Rows returns the total number of rows in the active buffer.
func (s *Screen) Rows() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.active.rows)
}

// View returns the `height` rows currently in view as a ready-to-render
// snapshot. On the main buffer it shows the bottom (live) `height` rows (or
// scrolls back into scrollback by the offset). On the alt buffer it is
// top-anchored: rows [0..height), padded with blanks if fewer were written —
// so content vim writes at the top stays visible instead of being pushed off
// by placeholder lines below it.
//
// View also remembers the rendering height (the scroll clamp ceiling on main,
// the fixed viewport height on alt). The returned slice aliases the grid's
// storage; callers must finish rendering before the next screen mutation.
func (s *Screen) View(height int) [][]Cell {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.active.view(height)
}

func (g *grid) view(height int) [][]Cell {
	total := len(g.rows)
	if !g.fixed {
		// Main: bottom-anchored window rows[total-height:].
		if height >= total {
			return g.rows
		}
		end := total - g.scrollOff
		if end < height {
			end = height
		}
		start := end - height
		return g.rows[start:end]
	}
	// Alt: top-anchored fixed viewport. Height is managed solely by
	// SetHeight/EnterAltScreen (not here) so this stays a pure read and is safe
	// under the read loop's writer / UI reader split.
	if height <= len(g.rows) {
		return g.rows[:height]
	}
	return g.rows
}

// ViewStart returns the absolute scrollback row shown at visible row 0 for the
// current view height. Main buffer: the bottom-anchor start (or scrolled-up
// offset). Alt buffer: always 0 (no scrollback, top-anchored).
func (s *Screen) ViewStart(height int) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.active.viewStart(height)
}

func (g *grid) viewStart(height int) int {
	if g.fixed {
		return 0
	}
	total := len(g.rows)
	if height >= total {
		return 0
	}
	end := total - g.scrollOff
	if end < height {
		end = height
	}
	return end - height
}

// TextRange extracts plain text from the current visible window. Coordinates
// are inclusive and are clamped to the visible window.
func (s *Screen) TextRange(height int, start, end Point) string {
	if height < 1 || s.cols < 1 {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	view := s.active.view(height)
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

// TextRangeAbs extracts plain text using absolute row coordinates within the
// active buffer. Columns are still terminal cell coordinates and all
// coordinates are inclusive.
func (s *Screen) TextRangeAbs(start, end Point) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows := s.active.rows
	if len(rows) == 0 || s.cols < 1 {
		return ""
	}
	start = clampPoint(start, len(rows), s.cols)
	end = clampPoint(end, len(rows), s.cols)
	if pointAfter(start, end) {
		start, end = end, start
	}
	var out []rune
	for r := start.Row; r <= end.Row; r++ {
		var row []Cell
		if r >= 0 && r < len(rows) {
			row = rows[r]
		}
		from, to := 0, s.cols-1
		if r == start.Row {
			from = start.Col
		}
		if r == end.Row {
			to = end.Col
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
func (s *Screen) Cursor() (int, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.active.curRow, s.active.curCol
}

// CursorInView returns the cursor's (row, col) translated into the coordinate
// space of View(height) — i.e. row is the index within the visible window, col
// is unchanged. It returns (-1, -1) when the cursor is outside the visible
// window (main buffer, scrolled away). On the alt buffer the cursor is always
// inside the fixed viewport.
func (s *Screen) CursorInView(height int) (int, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.active.cursorInView(height)
}

func (g *grid) cursorInView(height int) (int, int) {
	if g.fixed {
		// Cursor is always within the fixed viewport (rows are height-capped and
		// the cursor is clamped on write). Pure read; no resize here.
		return g.curRow, g.curCol
	}
	total := len(g.rows)
	if height >= total {
		return g.curRow, g.curCol
	}
	end := total - g.scrollOff
	start := end - height
	if g.curRow < start || g.curRow >= end {
		return -1, -1
	}
	return g.curRow - start, g.curCol
}

// ScrollOffset returns how many rows above the live bottom the view is scrolled
// back (0 = live output). Meaningful on the main buffer; always 0 on alt.
func (s *Screen) ScrollOffset() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.active.scrollOff
}

// OutputVersion changes whenever the remote output stream appends more data.
// UI selections are anchored to visible coordinates, so callers can use this to
// invalidate selections before copying stale coordinates from a shifted view.
func (s *Screen) OutputVersion() uint64 { return s.outputVersion.Load() }

// CurrentDir returns the remote shell working directory reported by terminal
// integration. An empty string means the shell has not reported one yet.
func (s *Screen) CurrentDir() string {
	dir := s.currentDir.Load()
	if dir == nil {
		return ""
	}
	return *dir
}

// PromptDir extracts a working directory from a conventional shell prompt on
// the cursor row, for example "root@server:/srv/app# ". This is a fallback for
// shells that never emit OSC 7 or only emit it once at login.
func (s *Screen) PromptDir(user string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows := s.active.rows
	if user == "" || s.active.curRow < 0 || s.active.curRow >= len(rows) {
		return ""
	}
	line := string(cellsText(rows[s.active.curRow], 0, s.cols-1))
	return promptDir(line, user)
}

func promptDir(line, user string) string {
	identity := user + "@"
	identityAt := strings.LastIndex(line, identity)
	if identityAt < 0 {
		return ""
	}
	afterIdentity := line[identityAt+len(identity):]
	colon := strings.IndexByte(afterIdentity, ':')
	if colon < 0 {
		return ""
	}
	rest := strings.TrimLeft(afterIdentity[colon+1:], " \t")
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case '#', '$', '%':
			if i == len(rest)-1 || rest[i+1] == ' ' {
				return validRemoteDir(strings.TrimSpace(rest[:i]))
			}
		}
	}
	return ""
}

// SetCurrentDir records a remote shell working directory. The parser calls
// this for OSC current-directory sequences; it is exported so Screen-backed
// terminal tests can model a shell that already reported its directory.
func (s *Screen) SetCurrentDir(dir string) {
	if dir == "" {
		return
	}
	value := dir
	s.currentDir.Store(&value)
}

// MarkOutput records that remote output changed the backing screen.
func (s *Screen) MarkOutput() { s.outputVersion.Add(1) }

// Scroll moves the view by delta rows (positive = further up into scrollback,
// negative = back toward live output). Main buffer only; no-op on alt.
func (s *Screen) Scroll(delta int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scrollToLocked(s.active.scrollOff + delta)
}

// ScrollTo sets the absolute scroll offset (rows above the live bottom). Main
// buffer only; no-op on alt. Clamped to [0, total-viewH] so the offset can't
// drift into a dead zone past the topmost reachable row.
func (s *Screen) ScrollTo(off int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scrollToLocked(off)
}

// scrollToLocked is the lock-free body of Scroll/ScrollTo; caller holds mu.
func (s *Screen) scrollToLocked(off int) {
	if s.active.fixed {
		return
	}
	max := s.scrollMax()
	switch {
	case off < 0:
		off = 0
	case off > max:
		off = max
	}
	s.active.scrollOff = off
}

// scrollMax is the largest meaningful offset given the known view height.
func (s *Screen) scrollMax() int {
	total := len(s.active.rows)
	if total < 1 {
		return 0
	}
	if s.active.viewH > 0 && total-s.active.viewH > 0 {
		return total - s.active.viewH
	}
	return total - 1
}

// ResetScroll re-anchors the view to live (bottom) output. Main buffer only.
func (s *Screen) ResetScroll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active.fixed {
		s.active.scrollOff = 0
	}
}

// Print writes a single rune at the cursor, honoring autowrap.
func (s *Screen) Print(ch rune, style Style) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active.print(ch, style)
}

func (g *grid) print(ch rune, style Style) {
	if g.pendingWrap {
		g.advanceRow()
		g.curCol = 0
		g.pendingWrap = false
	}
	g.ensureRow(g.curRow)
	if g.curCol >= g.cols {
		g.advanceRow()
		g.curCol = 0
		g.ensureRow(g.curRow)
	}
	g.rows[g.curRow][g.curCol] = Cell{Ch: ch, Style: style}
	g.curCol++
	if g.curCol >= g.cols {
		g.curCol = g.cols - 1
		g.pendingWrap = true
	}
}

// regionBounds returns the scroll region [top, bottom] (0-based, inclusive).
// If DECSTBM was never set (or reset), the region is the whole viewport.
func (g *grid) regionBounds() (int, int) {
	top, bot := g.scrollTop, g.scrollBottom
	if top < 0 {
		top = 0
	}
	if bot < 0 || bot >= g.height {
		bot = g.height - 1
	}
	if top > bot {
		top, bot = 0, g.height-1
	}
	return top, bot
}

// scrollUp scrolls the rows in [top, bottom] up by one: drop the top row of the
// region, shift the rest up, and blank the bottom row. The cursor stays on the
// bottom row.
func (g *grid) scrollUp(top, bottom int) {
	if bottom <= top {
		return
	}
	// Shift rows [top+1, bottom] up into [top, bottom-1].
	copy(g.rows[top:bottom], g.rows[top+1:bottom+1])
	// Blank the freed bottom row.
	g.rows[bottom] = make([]Cell, g.cols)
}

// scrollDown scrolls the rows in [top, bottom] down by one: drop the bottom row
// of the region, shift the rest down, and blank the top row. Used by CSI T
// (ScrollDown) and by reverse LineFeed.
func (g *grid) scrollDown(top, bottom int) {
	if bottom <= top {
		return
	}
	// Shift rows [top, bottom-1] down into [top+1, bottom].
	copy(g.rows[top+1:bottom+1], g.rows[top:bottom])
	// Blank the freed top row.
	g.rows[top] = make([]Cell, g.cols)
}

// insertLines inserts n blank rows at the cursor row, shifting the rows from
// the cursor down to the region bottom down by n (rows past the region bottom
// are lost). CSI Pn L = IL. The cursor row and below move down; the bottom of
// the region is clipped. Cursor stays put.
func (g *grid) insertLines(n, top, bottom int) {
	if n < 1 || bottom <= top {
		return
	}
	cur := g.curRow
	if cur < top {
		cur = top
	}
	if cur > bottom {
		return
	}
	avail := bottom - cur + 1
	if n > avail {
		n = avail
	}
	// Shift [cur, bottom-n] down into [cur+n, bottom].
	copy(g.rows[cur+n:bottom+1], g.rows[cur:bottom-n+1])
	// Blank the inserted rows [cur, cur+n-1].
	for r := cur; r < cur+n; r++ {
		g.rows[r] = make([]Cell, g.cols)
	}
}

// deleteLines deletes n rows starting at the cursor row, shifting the rows
// below up by n and blanking the freed rows at the region bottom. CSI Pn M =
// DL. Cursor stays put.
func (g *grid) deleteLines(n, top, bottom int) {
	if n < 1 || bottom <= top {
		return
	}
	cur := g.curRow
	if cur < top {
		cur = top
	}
	if cur > bottom {
		return
	}
	avail := bottom - cur + 1
	if n > avail {
		n = avail
	}
	// Shift [cur+n, bottom] up into [cur, bottom-n].
	copy(g.rows[cur:bottom-n+1], g.rows[cur+n:bottom+1])
	// Blank the freed rows [bottom-n+1, bottom].
	for r := bottom - n + 1; r <= bottom; r++ {
		g.rows[r] = make([]Cell, g.cols)
	}
}

// advanceRow moves the cursor down one row. On the main buffer this grows the
// grid (new scrollback row); on the alt buffer, when the cursor is at the
// scroll-region bottom, the region scrolls up by one (status/command lines
// outside the region stay put) instead of the cursor moving.
func (g *grid) advanceRow() {
	if !g.fixed {
		g.curRow++
		return
	}
	top, bot := g.regionBounds()
	if g.curRow >= bot {
		g.scrollUp(top, bot)
		if bot < g.curRow {
			g.curRow = bot
		}
		return
	}
	g.curRow++
}

// CarriageReturn moves the cursor to column 0 on the current row.
func (s *Screen) CarriageReturn() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active.curCol = 0
	s.active.pendingWrap = false
}

// LineFeed moves the cursor down one row. On the main buffer it grows the grid;
// on the alt buffer it scrolls within the viewport at the bottom. It does NOT
// reset the column (that is what CR is for); real terminals combine \r\n.
func (s *Screen) LineFeed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active.advanceRow()
	s.active.ensureRow(s.active.curRow)
	s.active.pendingWrap = false
}

// Backspace moves the cursor left one column (clamped at 0), clearing any
// pending wrap.
func (s *Screen) Backspace() {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.active
	if g.pendingWrap {
		g.pendingWrap = false
		return
	}
	if g.curCol > 0 {
		g.curCol--
	}
}

// Tab moves the cursor to the next multiple of 8.
func (s *Screen) Tab() {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.active
	next := ((g.curCol / 8) + 1) * 8
	if next >= g.cols {
		next = g.cols - 1
	}
	g.curCol = next
	g.pendingWrap = false
}

// SetCursor positions the cursor absolutely (0-based). Out-of-range values are
// clamped to the grid. On the alt buffer the row is clamped to the viewport
// height (it never grows).
func (s *Screen) SetCursor(row, col int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active.setCursor(row, col)
}

func (g *grid) setCursor(row, col int) {
	if row < 0 {
		row = 0
	}
	if col < 0 {
		col = 0
	}
	if col >= g.cols {
		col = g.cols - 1
	}
	if g.fixed && row >= g.height {
		row = g.height - 1
	}
	g.curRow = row
	g.ensureRow(row)
	g.curCol = col
	g.pendingWrap = false
}

// MoveCursor applies a relative cursor move by dRow/dCol (clamped >= 0).
func (s *Screen) MoveCursor(dRow, dCol int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.active
	g.setCursor(g.curRow+dRow, g.curCol+dCol)
}

// ClearLine erases part of the current line. mode: 0=cursor to end,
// 1=start to cursor (inclusive), 2=entire line.
func (s *Screen) ClearLine(mode int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.active
	g.ensureRow(g.curRow)
	row := g.rows[g.curRow]
	blank := Cell{}
	switch mode {
	case 0:
		for c := g.curCol; c < g.cols; c++ {
			row[c] = blank
		}
	case 1:
		for c := 0; c <= g.curCol && c < g.cols; c++ {
			row[c] = blank
		}
	case 2:
		for c := 0; c < g.cols; c++ {
			row[c] = blank
		}
	}
}

// DeleteChars deletes n chars at the cursor, shifting the rest of the line
// left and filling the freed cells at the right edge with blanks (CSI Pn P =
// DCH). The cursor position is unchanged. n defaults to 1 and is clamped to
// the remaining width.
func (s *Screen) DeleteChars(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.active
	if n < 1 {
		n = 1
	}
	g.ensureRow(g.curRow)
	row := g.rows[g.curRow]
	rem := g.cols - g.curCol
	if n > rem {
		n = rem
	}
	for c := g.curCol; c+n < g.cols; c++ {
		row[c] = row[c+n]
	}
	for c := g.cols - n; c < g.cols; c++ {
		row[c] = Cell{}
	}
}

// InsertChars inserts n blank chars at the cursor, shifting the existing chars
// to the right (chars past the right edge are lost). CSI Pn @ = ICH.
func (s *Screen) InsertChars(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.active
	if n < 1 {
		n = 1
	}
	g.ensureRow(g.curRow)
	row := g.rows[g.curRow]
	rem := g.cols - g.curCol
	if n > rem {
		n = rem
	}
	for c := g.cols - 1; c-n >= g.curCol; c-- {
		row[c] = row[c-n]
	}
	for c := g.curCol; c < g.curCol+n && c < g.cols; c++ {
		row[c] = Cell{}
	}
}

// EraseChars erases n chars at and after the cursor, replacing them with blanks
// without moving anything (CSI Pn X = ECH). Cursor is unchanged.
func (s *Screen) EraseChars(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.active
	if n < 1 {
		n = 1
	}
	g.ensureRow(g.curRow)
	row := g.rows[g.curRow]
	end := g.curCol + n
	if end > g.cols {
		end = g.cols
	}
	for c := g.curCol; c < end; c++ {
		row[c] = Cell{}
	}
}

// ClearScreen erases part of the screen. mode: 0=cursor to end of screen,
// 1=start of screen to cursor, 2=entire screen, 3=entire screen + scrollback.
//
// On the main buffer, mode 2/3 truncates scrollback and re-anchors the View at
// the cursor (so `clear` doesn't leave the view pinned to stale blank rows). On
// the alt buffer, mode 2/3 blanks the whole fixed viewport and homes the cursor
// (full-screen programs redraw from row 0 after clearing).
func (s *Screen) ClearScreen(mode int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active.clearScreen(mode, s.cols)
}

func (g *grid) clearScreen(mode int, cols int) {
	blank := Cell{}
	switch mode {
	case 0:
		g.ensureRow(g.curRow)
		for c := g.curCol; c < cols; c++ {
			g.rows[g.curRow][c] = blank
		}
		for r := g.curRow + 1; r < len(g.rows); r++ {
			for c := 0; c < cols; c++ {
				g.rows[r][c] = blank
			}
		}
	case 1:
		g.ensureRow(g.curRow)
		for r := 0; r < g.curRow; r++ {
			for c := 0; c < cols; c++ {
				g.rows[r][c] = blank
			}
		}
		for c := 0; c <= g.curCol && c < cols; c++ {
			g.rows[g.curRow][c] = blank
		}
	case 2, 3:
		if g.fixed {
			// Blank the whole viewport and home the cursor: full-screen apps
			// redraw from row 0 after a clear.
			for r := 0; r < len(g.rows); r++ {
				for c := 0; c < cols; c++ {
					g.rows[r][c] = blank
				}
			}
			g.curRow = 0
			g.curCol = 0
			g.pendingWrap = false
			return
		}
		// Main: truncate scrollback, keep a single blank row at the cursor's
		// row (clear normally homes the cursor first via CSI H).
		keep := g.curRow
		if keep < 0 {
			keep = 0
		}
		g.rows = g.rows[:0]
		for r := 0; r <= keep; r++ {
			g.rows = append(g.rows, make([]Cell, cols))
		}
	}
}

// ensureRow ensures curRow references a real row. Main buffer: grows the grid.
// Alt buffer: rows are pre-allocated to the fixed height, so this only clamps.
func (g *grid) ensureRow(r int) {
	if g.fixed {
		if r >= g.height {
			r = g.height - 1
		}
		if r < 0 {
			r = 0
		}
		return
	}
	for len(g.rows) <= r {
		g.rows = append(g.rows, make([]Cell, g.cols))
	}
}

// rebuild unconditionally rebuilds the grid's rows at the given height, all
// blank. Used when entering or resizing the alternate screen.
func (g *grid) rebuild(height int) {
	if height < 1 {
		height = 1
	}
	newRows := make([][]Cell, height)
	for i := 0; i < height; i++ {
		newRows[i] = make([]Cell, g.cols)
	}
	g.rows = newRows
	g.height = height
}

// EnterAltScreen switches to the alternate (fixed-height) buffer, sizing it to
// the last known view height. Used by ESC [ ? 1049 h. The main buffer and its
// cursor are preserved for LeaveAltScreen.
func (s *Screen) EnterAltScreen() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alt.rebuild(s.main.viewH)
	s.alt.curRow = 0
	s.alt.curCol = 0
	s.alt.pendingWrap = false
	s.alt.scrollOff = 0
	s.alt.scrollTop = -1
	s.alt.scrollBottom = -1
	s.active = s.alt
}

// LeaveAltScreen switches back to the main buffer, restoring its cursor and
// scrollback. Used by ESC [ ? 1049 l.
func (s *Screen) LeaveAltScreen() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = s.main
}

// SetScrollRegion sets the DECSTBM scroll region (CSI Pt;Pb r), 0-based
// inclusive. An empty/reset region (no params) clears it to the full viewport.
// Only affects the alt buffer (full-screen apps); the main buffer grows on
// LineFeed regardless.
func (s *Screen) SetScrollRegion(top, bottom int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.active
	if !g.fixed {
		return
	}
	if top < 0 {
		top = 0
	}
	if bottom < 0 || bottom >= g.height {
		bottom = g.height - 1
	}
	if top >= bottom {
		// Invalid/degenerate region: treat as full viewport.
		top, bottom = 0, g.height-1
	}
	g.scrollTop = top
	g.scrollBottom = bottom
	// DECSTBM also homes the cursor.
	g.curRow = 0
	g.curCol = 0
	g.pendingWrap = false
}

// InsertLines inserts n blank rows at the cursor row within the scroll region
// (CSI Pn L = IL). Alt buffer only; no-op on main.
func (s *Screen) InsertLines(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.active
	if !g.fixed {
		return
	}
	top, bot := g.regionBounds()
	g.insertLines(n, top, bot)
}

// DeleteLines deletes n rows at the cursor row within the scroll region
// (CSI Pn M = DL). Alt buffer only; no-op on main.
func (s *Screen) DeleteLines(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.active
	if !g.fixed {
		return
	}
	top, bot := g.regionBounds()
	g.deleteLines(n, top, bot)
}

// ScrollRegionUp scrolls the region up by n rows (CSI Pn S = SU). Alt buffer
// only; no-op on main.
func (s *Screen) ScrollRegionUp(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.active
	if !g.fixed || n < 1 {
		return
	}
	top, bot := g.regionBounds()
	for i := 0; i < n; i++ {
		g.scrollUp(top, bot)
	}
}

// ScrollRegionDown scrolls the region down by n rows (CSI Pn T = SD). Alt
// buffer only; no-op on main.
func (s *Screen) ScrollRegionDown(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.active
	if !g.fixed || n < 1 {
		return
	}
	top, bot := g.regionBounds()
	for i := 0; i < n; i++ {
		g.scrollDown(top, bot)
	}
}

// SetHeight syncs the buffer heights to a new terminal height. The alt buffer
// is resized to match; the main buffer just records the height as the scroll
// clamp ceiling. Called on terminal resize.
func (s *Screen) SetHeight(h int) {
	if h < 1 {
		h = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.main.viewH = h
	if s.alt.height != h {
		s.alt.rebuild(h)
	}
}
