package sshterm

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
)

// TestResizeConcurrentWithWrite exercises the concurrency surface added by the
// two-buffer refactor: the read loop (parser.Write → EnterAltScreen/Print/...)
// mutates s.active and the active grid's rows/cursor, while the UI goroutine
// reads via View/CursorInView/TextRangeAbs and resizes via Resize/SetHeight.
// Run with -race to catch unsynchronized access to s.active and grid state.
func TestResizeConcurrentWithWrite(t *testing.T) {
	reader, writer := io.Pipe()
	fake := &fakePtySession{
		out:    reader,
		stdin:  &bytes.Buffer{},
		stderr: &bytes.Buffer{},
	}
	term, err := NewTerminal(fake, 40, 10)
	if err != nil {
		t.Fatalf("NewTerminal: %v", err)
	}
	term.Start(context.Background())

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// UI goroutine: render + resize + query, as the real UI does every frame.
	wg.Add(1)
	go func() {
		defer wg.Done()
		h := 10
		for {
			select {
			case <-stop:
				return
			default:
			}
			screen := term.Screen()
			_ = screen.View(h)
			_, _ = screen.CursorInView(h)
			_ = screen.ViewStart(h)
			_ = screen.ScrollOffset()
			_ = screen.OutputVersion()
			_ = screen.TextRangeAbs(Point{Row: 0, Col: 0}, Point{Row: 2, Col: 5})
			// Occasionally resize — exercises SetHeight writing alt.rows while
			// the read loop may be writing to it.
			h = 8 + (h % 8)
			_ = term.Resize(40, h)
		}
	}()

	for i := 0; i < 50; i++ {
		if _, err := io.WriteString(writer, "\x1b[?1049h\x1b[Hline content\r\nmore\r\n\x1b[?1049lshell prompt\r\n"); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	<-term.Done()
	close(stop)
	wg.Wait()
}
