package sshterm

import "testing"

// TestParse_TopStyleRedraw reproduces the artifacts seen when running `top`
// in the embedded terminal: a stray `B` printed at the start of every line
// and between lines. The real cause is the G0 charset sequence ESC ( B that
// top emits during redraw: the parser consumed ESC and '(' but dropped the
// final byte 'B' into the screen as literal text.
func TestParse_TopStyleRedraw(t *testing.T) {
	s := NewScreen(80)
	p := NewParser(s)

	// A representative top redraw payload, led by the charset sequence that
	// was leaking. Each region line is preceded by ESC ( B in real top output.
	payload := []byte(
		"\x1b[?1049h\x1b[?25l" + // alt screen + hide cursor
			"\x1b(H" + // ESC ( H is also a charset sequence (just a different final byte)
			"\x1b(B" + // ESC ( B: select G0 = US-ASCII — the leaking sequence
			"\x1b[H" + // home
			"top - 17:30 up 1 day\x1b[K\r\n" +
			"\x1b(B" + // top re-asserts charset per region
			"  PID USER\x1b[K\r\n" +
			"\x1b(B" +
			"   23 root\x1b[K\r\n",
	)
	p.Write(payload)

	lines := screenLines(s)
	t.Logf("rendered lines:")
	for i, l := range lines {
		t.Logf("  [%d] %q", i, l)
	}

	// No stray 'B' (or 'H') should appear at line starts from charset sequences.
	for i, l := range lines {
		if containsStrayB(l) {
			t.Errorf("line %d contains a stray 'B' from ESC ( B: %q", i, l)
		}
	}
	foundHeader := false
	foundData := false
	for _, l := range lines {
		if strContains(l, "top - 17:30") {
			foundHeader = true
		}
		if strContains(l, "23 root") {
			foundData = true
		}
	}
	if !foundHeader {
		t.Error("header line 'top - 17:30' not found")
	}
	if !foundData {
		t.Error("data line '23 root' not found")
	}
}

// containsStrayB reports whether a row has a 'B' (the payload has no literal B
// in its text, so any B is a leaked final byte).
func containsStrayB(row string) bool {
	for _, r := range row {
		if r == 'B' {
			return true
		}
	}
	return false
}

func strContains(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestParse_CharsetSelection is a focused regression test for ESC ( B and
// related ESC I...I F sequences, independent of the top scenario.
func TestParse_CharsetSelection(t *testing.T) {
	cases := []string{
		"\x1b(B",  // G0 = US-ASCII
		"\x1b(0",  // G0 = DEC special graphics
		"\x1b)0",  // G1 = DEC special graphics
		"\x1b)B",  // G1 = US-ASCII
		"\x1b(BX", // followed by printable text
		"\x1b%GX", // ESC % G (UTF-8 level) + text
	}
	for _, seq := range cases {
		s := NewScreen(20)
		p := NewParser(s)
		p.Write([]byte(seq))
		got := rowText(s.rows[0])
		// Every case ends with literal text 'X' (or nothing). No 'B'/'0'/'G'
		// final bytes should leak.
		wantNoLeak := ""
		if strContains(seq, "X") {
			wantNoLeak = "X"
		}
		if got != wantNoLeak {
			t.Errorf("after %q row0 = %q, want %q (final byte leaked)", seq, got, wantNoLeak)
		}
	}
}
