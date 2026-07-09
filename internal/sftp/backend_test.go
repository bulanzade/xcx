package sftp

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

func TestLocalBackend_ReadDirAndStat(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.txt"), "hello")
	mustMkdir(t, filepath.Join(dir, "sub"))

	b := NewLocalBackend()
	entries, err := b.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	if len(entries) != 2 || entries[0].Name != "a.txt" || entries[1].Name != "sub" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
	if entries[0].IsDir || entries[0].Size != 5 {
		t.Fatalf("a.txt wrong: %+v", entries[0])
	}
	if !entries[1].IsDir {
		t.Fatalf("sub should be dir: %+v", entries[1])
	}

	st, err := b.Stat(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if st.IsDir || st.Size != 5 {
		t.Fatalf("stat a.txt wrong: %+v", st)
	}
}

func TestLocalBackend_MkdirRemoveRename(t *testing.T) {
	b := NewLocalBackend()
	dir := t.TempDir()

	target := filepath.Join(dir, "newdir")
	if err := b.Mkdir(target); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if st, err := b.Stat(target); err != nil || !st.IsDir {
		t.Fatalf("Mkdir not created: %v %+v", err, st)
	}

	// Rename a.txt -> b.txt
	src := filepath.Join(dir, "a.txt")
	dst := filepath.Join(dir, "b.txt")
	mustWrite(t, src, "data")
	if err := b.Rename(src, dst); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source should be gone after rename, got err=%v", err)
	}

	if err := b.Remove(dst); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("dst should be gone after remove, got err=%v", err)
	}
}

func TestCopy_LocalToLocal_WithProgress(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.bin")
	dst := filepath.Join(dir, "out.bin")
	payload := make([]byte, 100_000)
	for i := range payload {
		payload[i] = byte(i)
	}
	mustWrite(t, src, string(payload))

	var lastDone, lastTotal int64
	calls := 0
	b := NewLocalBackend()
	n, err := Copy(b, b, src, dst, func(done, total int64) {
		lastDone, lastTotal = done, total
		calls++
	})
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if n != int64(len(payload)) {
		t.Fatalf("copied %d bytes, want %d", n, len(payload))
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatal("destination content mismatch")
	}
	if lastDone != lastTotal || lastTotal != int64(len(payload)) {
		t.Fatalf("final progress = %d/%d, want %d/%d", lastDone, lastTotal, len(payload), len(payload))
	}
	if calls == 0 {
		t.Fatal("progress callback never invoked")
	}
}

func TestCopy_RejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	mustMkdir(t, sub)
	b := NewLocalBackend()
	if _, err := Copy(b, b, sub, filepath.Join(dir, "out"), nil); err == nil {
		t.Fatal("expected error copying a directory")
	}
}

// slowWriter wraps an io.Writer and sleeps per Write so a copy runs long
// enough to span several countingWriter.Write calls (letting the progress
// callback fire more than once during a single Copy).
type slowWriter struct {
	w     io.Writer
	delay time.Duration
}

func (s *slowWriter) Write(b []byte) (int, error) {
	time.Sleep(s.delay)
	return s.w.Write(b)
}
func (s *slowWriter) Close() error {
	if c, ok := s.w.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// slowBackend is a LocalBackend whose writes are delayed, keeping a copy in
// flight long enough to observe mid-transfer progress callbacks.
type slowBackend struct {
	LocalBackend
	delay time.Duration
}

func (b *slowBackend) Create(path string) (io.WriteCloser, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &slowWriter{w: f, delay: b.delay}, nil
}

// TestCopy_ReportsProgressMidTransfer verifies progress is reported while a
// copy is in flight (not only the final 100% report). A slowed writer keeps
// the copy running across several countingWriter.Write calls, so the progress
// callback must fire more than once.
//
// This exercises the local-fallback path (countingWriter over io.Copy). The
// sftp fast paths (WriteTo/ReadFrom with a counting wrapper) share the same
// counting type, so the per-Write callback behavior is the same; they are not
// covered here because they require a live *sftp.File (real SSH server).
func TestCopy_ReportsProgressMidTransfer(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.bin")
	dst := filepath.Join(dir, "out.bin")
	// Large enough that a 5ms/Write delay keeps the copy running across several
	// Writes. io.Copy uses 32KiB chunks, so ~3MB ≈ ~90 chunks ≈ 450ms.
	payload := bytes.Repeat([]byte("x"), 3_000_000)
	mustWrite(t, src, string(payload))

	srcB := NewLocalBackend()
	dstB := &slowBackend{delay: 5 * time.Millisecond}
	var mu sync.Mutex
	calls := 0
	var lastDone int64
	_, err := Copy(srcB, dstB, src, dst, func(done, total int64) {
		mu.Lock()
		calls++
		lastDone = done
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	mu.Lock()
	c, done := calls, lastDone
	mu.Unlock()
	if c < 2 {
		t.Fatalf("progress invoked %d time(s), want >=2 (should fire mid-copy)", c)
	}
	if done != int64(len(payload)) {
		t.Fatalf("last reported done = %d, want %d", done, len(payload))
	}
}

// TestCopy_RemovesDestinationOnError verifies that a failed copy cleans up the
// partial destination. UseConcurrentWrites can leave a file longer than the
// bytes actually written, and there's no resume support, so the partial output
// must be deleted to avoid a corrupt, sparse file masquerading as complete.
func TestCopy_RemovesDestinationOnError(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.bin")
	dst := filepath.Join(dir, "out.bin")
	// Larger than one 32KiB chunk so the copy spans multiple Writes and the
	// failing writer can error mid-stream (a tiny source would finish in one
	// Write and never trigger the failure).
	mustWrite(t, src, string(bytes.Repeat([]byte("x"), 100_000)))

	// failingBackend returns a writer that errors after the first Write, so the
	// destination file exists but the copy fails mid-stream.
	fb := &failingBackend{}
	_, err := Copy(NewLocalBackend(), fb, src, dst, nil)
	if err == nil {
		t.Fatal("expected copy error from failing writer")
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Fatalf("partial destination should be removed on failure, stat err=%v", statErr)
	}
}

type failingBackend struct {
	LocalBackend
}

func (b *failingBackend) Create(path string) (io.WriteCloser, error) {
	return &failingWriter{path: path}, nil
}

type failingWriter struct {
	path string
	done bool
}

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.done {
		return 0, fmt.Errorf("simulated write failure")
	}
	// First Write succeeds so the file is created, then subsequent ones fail.
	w.done = true
	if err := os.WriteFile(w.path, p, 0o644); err != nil {
		return 0, err
	}
	return len(p), nil
}
func (w *failingWriter) Close() error { return nil }

// TestCountingReaderExposesSize is the contract that makes upload concurrency
// work: pkg/sftp's File.ReadFrom gates concurrency on sizing the reader via the
// `interface{ Size() int64 }` type switch. countingReader must satisfy it
// (returning the source size), or ReadFrom silently falls back to one
// synchronous write per chunk and uploads are slow. This locks that contract.
func TestCountingReaderExposesSize(t *testing.T) {
	r := &countingReader{size: 1 << 20}
	sizer, ok := interface{}(r).(interface{ Size() int64 })
	if !ok {
		t.Fatal("countingReader must implement interface{ Size() int64 } so pkg/sftp's ReadFrom enables concurrency")
	}
	if got := sizer.Size(); got != 1<<20 {
		t.Fatalf("Size() = %d, want %d", got, 1<<20)
	}
}

// --- helpers --------------------------------------------------------------

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}
