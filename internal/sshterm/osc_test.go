package sshterm

import (
	"bytes"
	"testing"
)

// TestOSC_ColorQueryNoLeak is the core regression test for the bug where opening
// an empty file in vim showed "10;?11;?" at the top. vim queries the terminal's
// foreground/background colors via OSC 10;? and OSC 11;?, terminated by BEL.
// Before the fix, the parser left the OSC state immediately (on ESC ]) and the
// payloads "10;?" and "11;?" printed as visible text.
func TestOSC_ColorQueryNoLeak(t *testing.T) {
	s := NewScreen(40)
	p := NewParser(s)
	p.Write([]byte("\x1b]10;?\x07")) // OSC 10;? (fg) BEL
	p.Write([]byte("\x1b]11;?\x07")) // OSC 11;? (bg) BEL
	if got := rowText(s.rows[0]); got != "" {
		t.Fatalf("OSC color queries leaked as text: %q, want empty", got)
	}
}

// TestOSC_STTerminator verifies OSC sequences terminated by ST (ESC \) are also
// consumed (some terminals/programs use ST instead of BEL).
func TestOSC_STTerminator(t *testing.T) {
	s := NewScreen(40)
	p := NewParser(s)
	p.Write([]byte("\x1b]0;window-title\x1b\\")) // OSC 0 (set title), ST-terminated
	p.Write([]byte("X"))
	got := rowText(s.rows[0])
	if got != "X" {
		t.Fatalf("after ST-terminated OSC row0 = %q, want \"X\" (OSC leaked)", got)
	}
}

// TestOSC_FollowedByText verifies the parser returns to ground after an OSC and
// renders subsequent text correctly.
func TestOSC_FollowedByText(t *testing.T) {
	s := NewScreen(40)
	p := NewParser(s)
	p.Write([]byte("\x1b]10;?\x07hello"))
	if got := rowText(s.rows[0]); got != "hello" {
		t.Fatalf("row0 = %q, want \"hello\"", got)
	}
}

// TestOSC_ColorQueryResponse verifies that, with a responder wired, OSC 10;?
// and 11;? produce color replies so vim/other apps get a palette answer.
func TestOSC_ColorQueryResponse(t *testing.T) {
	s := NewScreen(40)
	p := NewParser(s)
	buf := &bytes.Buffer{}
	p.SetResponder(func(b []byte) { buf.Write(b) })
	p.Write([]byte("\x1b]10;?\x07"))
	p.Write([]byte("\x1b]11;?\x07"))
	got := buf.String()
	if !bytes.Contains([]byte(got), []byte("\x1b]10;rgb:")) {
		t.Fatalf("missing OSC 10 fg response: %q", got)
	}
	if !bytes.Contains([]byte(got), []byte("\x1b]11;rgb:")) {
		t.Fatalf("missing OSC 11 bg response: %q", got)
	}
}

// TestOSC_NoResponderStillConsumed verifies that even without a responder the
// OSC payload is consumed (no leak).
func TestOSC_NoResponderStillConsumed(t *testing.T) {
	s := NewScreen(40)
	p := NewParser(s) // no responder
	p.Write([]byte("\x1b]10;?\x07\x1b]11;?\x07\x1b]0;title\x07"))
	if got := rowText(s.rows[0]); got != "" {
		t.Fatalf("OSC leaked with no responder: %q", got)
	}
}
