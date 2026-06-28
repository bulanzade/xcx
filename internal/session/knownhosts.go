package session

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// HostKeyError describes a host-key decision that needs a human: either the
// host is unknown (and the user must decide whether to trust it) or the
// recorded key does not match the one offered (potential MITM).
type HostKeyError struct {
	Host        string // "addr:port"
	Fingerprint string // SHA256:...
	Unknown     bool   // true = first contact, false = mismatch
}

func (e *HostKeyError) Error() string {
	if e.Unknown {
		return fmt.Sprintf("unknown host %s (fingerprint %s) — needs confirmation", e.Host, e.Fingerprint)
	}
	return fmt.Sprintf("host key mismatch for %s (offered %s) — possible MITM", e.Host, e.Fingerprint)
}

// A HostKeyDB persists trusted host keys and looks them up. Implementations:
// FileHostKeyDB (production) and memHostKeyDB (tests).
type HostKeyDB interface {
	// Lookup returns the trusted key for host, or nil if none is recorded.
	Lookup(host string) (ssh.PublicKey, error)
	// Add records key as trusted for host.
	Add(host string, key ssh.PublicKey) error
}

// HostKeyVerifier is a HostKeyCallback factory: given a host, it returns an
// ssh.HostKeyCallback that consults db. Unknown hosts return *HostKeyError
// (Unknown=true); mismatches return *HostKeyError (Unknown=false). The UI
// catches *HostKeyError, asks the user, and on trust calls db.Add + retries.
type HostKeyVerifier struct {
	DB HostKeyDB
	// trustOnUnknown, when true, makes the callback persist the offered key on
	// first contact (instead of returning an *HostKeyError) and accept it.
	// Mismatches are still rejected. Used after the user confirms an unknown host.
	trustOnUnknown bool
}

// TrustOnUnknown returns a verifier whose callback, on an unknown host,
// records the offered key into the DB and accepts it. Mismatches are still
// rejected. Use this after the user has explicitly confirmed an unknown host.
func (v *HostKeyVerifier) TrustOnUnknown() *HostKeyVerifier {
	return &HostKeyVerifier{DB: v.DB, trustOnUnknown: true}
}

func (v *HostKeyVerifier) Callback() ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		// hostname is the "addr:port" we dialed.
		fp := ssh.FingerprintSHA256(key)
		known, err := v.DB.Lookup(hostname)
		if err != nil {
			return fmt.Errorf("lookup known_hosts: %w", err)
		}
		if known == nil {
			if v.trustOnUnknown {
				if err := v.DB.Add(hostname, key); err != nil {
					return fmt.Errorf("record known_hosts: %w", err)
				}
				return nil
			}
			return &HostKeyError{Host: hostname, Fingerprint: fp, Unknown: true}
		}
		if !keyEqual(known, key) {
			return &HostKeyError{Host: hostname, Fingerprint: fp, Unknown: false}
		}
		return nil
	}
}

// TrustAndRetry is a convenience for the UI: after the user confirms an
// unknown host, persist it. The caller then retries Connect.
func (v *HostKeyVerifier) Trust(host string, key ssh.PublicKey) error {
	return v.DB.Add(host, key)
}

func keyEqual(a, b ssh.PublicKey) bool {
	aw := a.Marshal()
	bw := b.Marshal()
	if len(aw) != len(bw) {
		return false
	}
	for i := range aw {
		if aw[i] != bw[i] {
			return false
		}
	}
	return true
}

// IsHostKeyError reports whether err is a *HostKeyError needing user input.
func IsHostKeyError(err error) (*HostKeyError, bool) {
	var hke *HostKeyError
	if errors.As(err, &hke) {
		return hke, true
	}
	return nil, false
}

// --- File-backed known_hosts store ---------------------------------------

// FileHostKeyDB stores trusted keys in OpenSSH known_hosts line format:
//
//	<host> <key-type> <base64-key>
//
// One entry per line (we don't merge multiple keys per host).
type FileHostKeyDB struct {
	Path string
}

func NewFileHostKeyDB(path string) *FileHostKeyDB {
	return &FileHostKeyDB{Path: path}
}

func (f *FileHostKeyDB) Lookup(host string) (ssh.PublicKey, error) {
	lines, err := knownHostLines(f.Path)
	if err != nil {
		return nil, err
	}
	for _, l := range lines {
		fields := strings.Fields(l)
		if len(fields) < 3 || fields[0] != host {
			continue
		}
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(fields[1] + " " + fields[2]))
		if err != nil {
			return nil, fmt.Errorf("parse known_hosts line: %w", err)
		}
		return pub, nil
	}
	return nil, nil
}

func (f *FileHostKeyDB) Add(host string, key ssh.PublicKey) error {
	if err := os.MkdirAll(filepathDir(f.Path), 0o700); err != nil {
		return err
	}
	line := fmt.Sprintf("%s %s\n", host, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))))
	// Append; create with 0600 if new.
	flag := os.O_APPEND | os.O_CREATE | os.O_WRONLY
	fl, err := os.OpenFile(f.Path, flag, 0o600)
	if err != nil {
		return err
	}
	defer fl.Close()
	_, err = fl.WriteString(line)
	return err
}

func knownHostLines(path string) ([]string, error) {
	fl, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer fl.Close()
	var lines []string
	sc := bufio.NewScanner(fl)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		s := strings.TrimSpace(sc.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		lines = append(lines, s)
	}
	return lines, sc.Err()
}

// filepathDir mirrors the vault package's minimal dir helper without pulling
// path/filepath at call sites that already import it elsewhere.
func filepathDir(p string) string { return filepath.Dir(p) }
