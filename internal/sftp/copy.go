package sftp

import (
	"fmt"
	"io"
)

// ProgressFunc reports bytes transferred vs. total during a copy. It is
// invoked periodically (driven by the caller, e.g. a tracking writer).
type ProgressFunc func(done, total int64)

// Copy transfers the file at srcPath on src to dstPath on dst, invoking prog
// as bytes flow. total is taken from a Stat of the source. It is the single
// primitive the transfer queue uses for both upload and download.
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
	defer out.Close()

	var w io.Writer = out
	if prog != nil {
		w = &progressWriter{w: out, total: total, prog: prog}
	}
	n, err := io.Copy(w, in)
	if err != nil {
		return n, err
	}
	// Ensure trailing progress reaches 100% on success.
	if prog != nil {
		prog(n, total)
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return n, err
}

// progressWriter wraps an io.Writer and reports progress after each Write.
// io.Copy writes in 32KiB chunks by default, so this gives ~32KiB resolution.
type progressWriter struct {
	w     io.Writer
	total int64
	done  int64
	prog  ProgressFunc
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.done += int64(n)
	if p.prog != nil {
		p.prog(p.done, p.total)
	}
	return n, err
}
