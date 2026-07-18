package sshterm

import "testing"

// TestLineEdit_DeleteCharMidLine is a regression test for the bug where deleting
// a character mid-line left a duplicate of the last character on screen. The
// user typed "docker log", moved the cursor onto 'o', pressed backspace, and
// saw "docker ogg" (an extra 'g') instead of "docker og".
//
// Root cause: readline redraws the line with CSI P (DCH, delete character) or
// CSI K (erase to EOL) after repositioning; the parser handled neither DCH/ICH
// nor ECH, so characters were never actually removed — they were left behind
// and appeared duplicated.
func TestLineEdit_DeleteCharMidLine(t *testing.T) {
	s := NewScreen(40)
	p := NewParser(s)
	p.Write([]byte("docker log"))
	// Layout: d(0) o(1) c(2) k(3) e(4) r(5) ' '(6) l(7) o(8) g(9).
	// Cursor on 'o' (0-based col 8): CSI 9 G (1-based).
	p.Write([]byte("\x1b[9G"))
	// Backspace: move left onto 'l' (col 7), then DCH deletes it -> "docker og".
	p.Write([]byte("\x1b[D")) // left onto 'l'
	p.Write([]byte("\x1b[P")) // DCH: delete char at col 7

	got := rowText(screenRows(s)[0])
	if got != "docker og" {
		t.Fatalf("after DCH delete: row0 = %q, want \"docker og\"", got)
	}
}

// TestLineEdit_DCH_shiftsAndBlanksTail verifies DCH shifts left and clears the
// freed cell at the right edge (no leftover duplicate).
func TestLineEdit_DCH_shiftsAndBlanksTail(t *testing.T) {
	s := NewScreen(5)
	p := NewParser(s)
	p.Write([]byte("abcde"))   // fill the row
	p.Write([]byte("\x1b[2G")) // cursor to col 1 (0-based) -> on 'b'
	p.Write([]byte("\x1b[P"))  // delete 'b' -> "acde" + blank
	got := rowText(screenRows(s)[0])
	if got != "acde" {
		t.Fatalf("DCH result = %q, want \"acde\" (no duplicate e)", got)
	}
}

// TestLineEdit_ECH_blanksAtCursor verifies CSI X (erase chars) blanks cells
// without shifting.
func TestLineEdit_ECH_blanksAtCursor(t *testing.T) {
	s := NewScreen(8)
	p := NewParser(s)
	p.Write([]byte("abcdefgh"))
	p.Write([]byte("\x1b[3G")) // cursor to col 2 (0-based) -> on 'c'
	p.Write([]byte("\x1b[2X")) // erase 2 chars -> "ab__efgh"
	got := rowText(screenRows(s)[0])
	if got != "ab  efgh" {
		t.Fatalf("ECH result = %q, want \"ab  efgh\"", got)
	}
}

// TestLineEdit_ICH_insertsBlanks verifies CSI @ (insert chars) shifts right.
func TestLineEdit_ICH_insertsBlanks(t *testing.T) {
	s := NewScreen(8)
	p := NewParser(s)
	p.Write([]byte("abc"))
	p.Write([]byte("\x1b[1G")) // cursor to col 0 -> on 'a'
	p.Write([]byte("\x1b[@"))  // insert 1 char -> "_abc____"
	got := rowText(screenRows(s)[0])
	if got != " abc" {
		t.Fatalf("ICH result = %q, want \" abc\"", got)
	}
}
