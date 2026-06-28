// Package sftp wraps the SFTP subsystem over an SSH session and the local
// filesystem behind a single Backend interface, so the dual-pane UI and the
// transfer queue treat local and remote sides symmetrically.
package sftp

import (
	"io"
	"os"
	"time"

	sftppkg "github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Entry is a normalized file/dir entry used by the dual-pane UI.
type Entry struct {
	Name    string
	IsDir   bool
	Size    int64
	ModTime time.Time
}

// Backend abstracts a browsable file system side (local or remote). Both
// panes of the UI and the transfer queue operate against this interface.
type Backend interface {
	// ReadDir lists the entries of dir. The returned slice is unsorted.
	ReadDir(dir string) ([]Entry, error)
	// Stat returns metadata for a single path.
	Stat(path string) (Entry, error)
	// OpenRead opens path for reading; the caller must close the reader.
	OpenRead(path string) (io.ReadCloser, error)
	// Create opens path for writing (truncating); the caller must close.
	Create(path string) (io.WriteCloser, error)
	// Mkdir creates a directory.
	Mkdir(path string) error
	// Remove removes a file or empty directory.
	Remove(path string) error
	// Rename renames or moves a path.
	Rename(oldname, newname string) error
}

// --- local backend (os package) -----------------------------------------

// LocalBackend is a Backend backed by the host's file system.
type LocalBackend struct{}

func NewLocalBackend() *LocalBackend { return &LocalBackend{} }

func (LocalBackend) ReadDir(dir string) ([]Entry, error) {
	infos, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(infos))
	for _, inf := range infos {
		info, err := inf.Info()
		if err != nil {
			continue
		}
		out = append(out, entryFromFileInfo(info))
	}
	return out, nil
}

func (LocalBackend) Stat(path string) (Entry, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Entry{}, err
	}
	return entryFromFileInfo(info), nil
}

func (LocalBackend) OpenRead(path string) (io.ReadCloser, error) { return os.Open(path) }

func (LocalBackend) Create(path string) (io.WriteCloser, error) {
	return os.Create(path)
}

func (LocalBackend) Mkdir(path string) error {
	return os.MkdirAll(path, 0o755)
}

func (LocalBackend) Remove(path string) error { return os.Remove(path) }

func (LocalBackend) Rename(oldname, newname string) error { return os.Rename(oldname, newname) }

func entryFromFileInfo(info os.FileInfo) Entry {
	return Entry{
		Name:    info.Name(),
		IsDir:   info.IsDir(),
		Size:    info.Size(),
		ModTime: info.ModTime(),
	}
}

// --- remote backend (pkg/sftp over *ssh.Client) --------------------------

// RemoteBackend is a Backend backed by an SFTP subsystem on an SSH client.
type RemoteBackend struct {
	c *sftppkg.Client
}

// NewRemoteBackend opens the SFTP subsystem on c and returns a Backend.
func NewRemoteBackend(c *ssh.Client) (*RemoteBackend, error) {
	sc, err := sftppkg.NewClient(c)
	if err != nil {
		return nil, err
	}
	return &RemoteBackend{c: sc}, nil
}

// Close releases the underlying SFTP client.
func (r *RemoteBackend) Close() error { return r.c.Close() }

func (r *RemoteBackend) ReadDir(dir string) ([]Entry, error) {
	infos, err := r.c.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(infos))
	for _, info := range infos {
		out = append(out, entryFromFileInfo(info))
	}
	return out, nil
}

func (r *RemoteBackend) Stat(path string) (Entry, error) {
	info, err := r.c.Stat(path)
	if err != nil {
		return Entry{}, err
	}
	return entryFromFileInfo(info), nil
}

func (r *RemoteBackend) OpenRead(path string) (io.ReadCloser, error) { return r.c.Open(path) }

func (r *RemoteBackend) Create(path string) (io.WriteCloser, error) {
	return r.c.Create(path)
}

func (r *RemoteBackend) Mkdir(path string) error { return r.c.Mkdir(path) }

func (r *RemoteBackend) Remove(path string) error { return r.c.Remove(path) }

func (r *RemoteBackend) Rename(oldname, newname string) error { return r.c.Rename(oldname, newname) }
