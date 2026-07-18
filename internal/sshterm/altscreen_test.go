package sshterm

import (
	"strings"
	"testing"
)

// TestParse_VimAltScreenClears reproduces "vim opens a 4-line file but the
// display starts from the second line". The shell prompt is on row 0; vim
// switches to the alternate screen with ESC [ ? 1049 h, which a real xterm
// treats as "save cursor, switch to a cleared alternate buffer, home". The
// parser consumed 1049h but did nothing, so the shell's row 0 (the prompt)
// was not cleared and vim's first content row landed on top of stale output
// — making it look like content started one line down.
//
// After the fix, 1049h must clear the visible screen and home the cursor so
// vim starts writing at row 0.
func TestParse_VimAltScreenClears(t *testing.T) {
	s := NewScreen(20)
	p := NewParser(s)

	// Shell has printed a prompt on the first line, cursor now at end of it.
	s.SetHeight(5) // establish terminal height (View no longer seeds it)
	p.Write([]byte("$ "))
	// Cursor is at row 0, col 2. Now the user runs vim, which switches to the
	// alternate screen and renders a 4-line file.
	p.Write([]byte(
		"\x1b[?1049h" + // alt screen: should clear + home
			"\x1b[?25l" + // hide cursor
			"\x1b[H" + // home
			"a\x1b[0m\r\n" +
			"b\x1b[0m\r\n" +
			"c\x1b[0m\r\n" +
			"d\x1b[0m",
	))

	// Render the bottom 5 rows of scrollback and check content placement.
	got := screenLines(s)

	// The first content line 'a' must be on the topmost rendered row, not
	// preceded by the stale shell prompt "$" on the same logical screen.
	top := strings.TrimSpace(strings.Join(got, "|"))
	if strings.Contains(top, "$") {
		t.Errorf("stale shell prompt survived alt-screen switch: %q", got)
	}
	// 'a' must be the first non-blank line of the live screen.
	firstNonBlank := ""
	for _, l := range got {
		if strings.TrimSpace(l) != "" {
			firstNonBlank = strings.TrimSpace(l)
			break
		}
	}
	if firstNonBlank != "a" {
		t.Errorf("first content line = %q, want 'a' (vim content should start at row 0)", firstNonBlank)
	}
}
