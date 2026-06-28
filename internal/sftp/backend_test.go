package sftp

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
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
