package sftp

import (
	"fmt"
	"io"

	sftppkg "github.com/pkg/sftp"
)

// ProgressFunc reports bytes transferred vs. total during a copy. It is
// invoked periodically (driven by the caller, e.g. a tracking reader/writer).
type ProgressFunc func(done, total int64)

// Copy transfers the file at srcPath on src to dstPath on dst, invoking prog
// as bytes flow. total is taken from a Stat of the source. It is the single
// primitive the transfer queue uses for both upload and download.
//
// Speed is driven by pkg/sftp's concurrent pipeline: when one side is a remote
// *sftp.File we dispatch explicitly onto its WriteTo (download) or ReadFrom
// (upload) methods — those run dozens of in-flight requests that fill the RTT
// pipe, the same way the sftp CLI reaches line speed. Counting is done by
// wrapping the *other* side (a plain reader/writer) so we never break the
// fast path or wrap the concurrent file itself.
//
// On error the destination is removed: UseConcurrentWrites can leave a file
// longer than the successfully written range, and there's no resume support, so
// deleting the partial output is the simplest correct cleanup.
func Copy(src, dst Backend, srcPath, dstPath string, prog ProgressFunc) (int64, error) {
	info, err := src.Stat(srcPath)
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", srcPath, err)
	}
	if info.IsDir {
		return 0, fmt.Errorf("source %s is a directory", srcPath)
	}
	total := info.Size

	in, err := src.OpenRead(srcPath)
	if err != nil {
		return 0, fmt.Errorf("open read %s: %w", srcPath, err)
	}
	defer in.Close()

	out, err := dst.Create(dstPath)
	if err != nil {
		return 0, fmt.Errorf("create %s: %w", dstPath, err)
	}

	n, copyErr := copyDispatch(out, in, total, prog)
	if cerr := out.Close(); copyErr == nil {
		copyErr = cerr
	}
	if copyErr != nil {
		// Concurrent writes may have advanced the file past the last good byte;
		// without resume support the partial output is useless, so drop it.
		_ = dst.Remove(dstPath)
		return n, copyErr
	}
	// Final progress so the bar reaches 100% on success.
	if prog != nil {
		prog(n, total)
	}
	return n, nil
}

// copyDispatch selects the fastest copy path for the given handles and reports
// progress via a counting reader/writer on the non-fast-path side.
//
//   - download (src is *sftp.File): src.WriteTo(countingWriter{dst})
//   - upload   (dst is *sftp.File): dst.ReadFrom(countingReader{src})
//   - fallback: io.Copy(countingWriter{dst}, src)
//
// Counting wraps only the plain side: WriteTo/ReadFrom pull from / push to it,
// so the count reflects bytes actually fed into the transfer pipeline without
// interfering with pkg/sftp's concurrent requests.
//
// Both fast paths let pkg/sftp choose the concurrency itself:
//   - Download: WriteTo sizes the SOURCE file (fstat/stat) to decide, so
//     wrapping dst doesn't affect it.
//   - Upload: ReadFrom sizes the reader via Len/Size/Stat. countingReader
//     implements Size() (returning total), so ReadFrom sees the size and picks
//     the right concurrency — concurrent for large files, serial for small
//     ones — instead of a hardcoded worker count.
func copyDispatch(out io.Writer, in io.Reader, total int64, prog ProgressFunc) (int64, error) {
	switch srcFile := in.(type) {
	case *sftppkg.File:
		// Download: remote read, local write.
		w := &countingWriter{w: out, total: total, prog: prog}
		return srcFile.WriteTo(w)
	}
	if dstFile, ok := out.(*sftppkg.File); ok {
		// Upload: local read, remote write. countingReader.Size() lets ReadFrom
		// auto-enable concurrency based on size (no hardcoded worker count).
		r := &countingReader{r: in, size: total, total: total, prog: prog}
		return dstFile.ReadFrom(r)
	}
	// Neither side is remote: plain copy with a counting writer.
	w := &countingWriter{w: out, total: total, prog: prog}
	return io.Copy(w, in)
}

// countingReader wraps an io.Reader, reports progress after each Read, and
// exposes the source size via Size() so pkg/sftp's ReadFrom can decide whether
// to upload concurrently (large file → many in-flight writes) or serially
// (small file → no worker-pool overhead). Without Size(), ReadFrom cannot size
// the reader and would fall back to serial writes for every upload.
type countingReader struct {
	r     io.Reader
	size  int64
	total int64
	done  int64
	prog  ProgressFunc
}

// Size returns the total byte count of the wrapped stream, satisfying the
// interface pkg/sftp's ReadFrom uses to gate upload concurrency.
func (c *countingReader) Size() int64 { return c.size }

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.done += int64(n)
	if c.prog != nil {
		c.prog(c.done, c.total)
	}
	return n, err
}

// countingWriter wraps an io.Writer and reports progress after each Write.
type countingWriter struct {
	w     io.Writer
	total int64
	done  int64
	prog  ProgressFunc
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.done += int64(n)
	if c.prog != nil {
		c.prog(c.done, c.total)
	}
	return n, err
}
