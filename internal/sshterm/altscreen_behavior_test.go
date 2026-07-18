package sshterm

import (
	"slices"
	"testing"
)

// TestAltScreen_LineFeedScrollsViewport verifies the alt buffer scrolls within
// its fixed viewport instead of growing. vim/less write content top-down and
// expect the viewport to be exactly `height` rows; if the alt buffer grew like
// the main one, content written past `height` would push the top off-screen
// (the original bug).
func TestAltScreen_LineFeedScrollsViewport(t *testing.T) {
	const h = 4
	s := NewScreen(10)
	s.SetHeight(h) // establish terminal height (View no longer seeds it)
	p := NewParser(s)

	p.Write([]byte("\x1b[?1049h\x1b[H"))
	// Write more lines than the viewport height: 5 lines into a 4-row viewport.
	p.Write([]byte("L1\r\nL2\r\nL3\r\nL4\r\nL5"))

	// L1 scrolled off the top; L2..L5 remain visible top-to-bottom.
	if got, want := screenLines(s), []string{"L2", "L3", "L4", "L5"}; !slices.Equal(got, want) {
		t.Fatalf("view = %v, want %v", got, want)
	}
}

// TestAltScreen_LeaveRestoresMainBuffer verifies that leaving the alt screen
// (ESC [ ? 1049 l) restores the main buffer's content and cursor — the shell's
// previous output must reappear after vim/less exit.
func TestAltScreen_LeaveRestoresMainBuffer(t *testing.T) {
	const h = 5
	s := NewScreen(20)
	s.SetHeight(h)
	p := NewParser(s)

	// Shell output on the main buffer.
	p.Write([]byte("shell output\r\n"))
	mainRows := s.Rows()

	// Enter alt, write vim content, leave.
	p.Write([]byte("\x1b[?1049h\x1b[Hvim line 1\r\nvim line 2"))
	p.Write([]byte("\x1b[?1049l"))

	// After leaving, the main buffer is active again and its content survives.
	if got := s.Rows(); got != mainRows {
		t.Errorf("main row count changed across alt switch: %d -> %d", mainRows, got)
	}
	if got := screenLines(s); !slices.Contains(got, "shell output") {
		t.Errorf("main buffer content lost after alt round-trip: %v", got)
	}
}

// TestAltScreen_ResizeSyncsHeight verifies that resizing the terminal while in
// the alt buffer adjusts the fixed viewport (full-screen apps expect the
// viewport to track the terminal size).
func TestAltScreen_ResizeSyncsHeight(t *testing.T) {
	s := NewScreen(20)
	s.SetHeight(5)
	p := NewParser(s)
	p.Write([]byte("\x1b[?1049h")) // enter alt with height 5

	if got := s.Rows(); got != 5 {
		t.Fatalf("alt height = %d, want 5", got)
	}

	s.SetHeight(8)
	if got := s.Rows(); got != 8 {
		t.Fatalf("after resize alt height = %d, want 8", got)
	}
}
