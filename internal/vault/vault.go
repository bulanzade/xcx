// Package vault provides AES-256-GCM encrypted storage for SSH host
// connection configurations. The master password is run through Argon2id
// to derive the encryption key.
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/crypto/argon2"
	"gopkg.in/yaml.v3"
)

// File layout on disk (all little-endian where applicable):
//
//	[ salt: 16B ][ nonce: 12B ][ ciphertext: rest ]
//
// The salt and nonce are stored in the clear; the ciphertext is AES-256-GCM
// over the YAML-serialized Vault (GCM's auth tag covers it as a suffix).
const (
	saltLen  = 16
	nonceLen = 12
	keyLen   = 32 // AES-256

	// Argon2id parameters. Deliberately modest so unlock stays snappy on
	// low-memory machines while still resisting offline brute force.
	argonTime    = 2
	argonMemory  = 64 * 1024 // 64 MB
	argonThreads = 2
)

// Errors returned by this package.
var (
	// ErrInvalidPassword is returned when the master password cannot
	// decrypt the vault (wrong password or tampered ciphertext).
	ErrInvalidPassword = errors.New("invalid master password or corrupted vault")
	// ErrCorrupt is returned when the on-disk file is too short or
	// malformed to even attempt decryption.
	ErrCorrupt = errors.New("vault file is corrupt or truncated")
)

// Vault is the in-memory, decrypted representation of the config file.
type Vault struct {
	Groups []Group `yaml:"groups"`
}

// Group is a named collection of hosts (a folder in the host tree).
type Group struct {
	Name  string `yaml:"name"`
	Hosts []Host `yaml:"hosts"`
}

// Host describes a single SSH target.
type Host struct {
	Name string `yaml:"name"` // display name
	Addr string `yaml:"addr"` // hostname or IP
	Port int    `yaml:"port"` // default 22
	User string `yaml:"user"`
	Auth Auth   `yaml:"auth"`
}

// Auth holds the credentials used to authenticate a host.
type Auth struct {
	Type       string `yaml:"type"`                 // "password" | "key"
	Password   string `yaml:"password,omitempty"`   // when Type == "password"
	KeyPath    string `yaml:"key_path,omitempty"`   // private key file path
	Passphrase string `yaml:"passphrase,omitempty"` // private key passphrase (optional)
}

// Save serializes v to YAML, encrypts it with a fresh salt+nonce derived
// from masterPassword, and writes it atomically to path.
func Save(path, masterPassword string, v *Vault) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("vault: marshal: %w", err)
	}

	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return fmt.Errorf("vault: read salt: %w", err)
	}
	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("vault: read nonce: %w", err)
	}

	key := deriveKey(masterPassword, salt)
	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("vault: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("vault: new gcm: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, data, nil)

	out := make([]byte, 0, saltLen+nonceLen+len(ciphertext))
	out = append(out, salt...)
	out = append(out, nonce...)
	out = append(out, ciphertext...)

	return atomicWrite(path, out, 0o600)
}

// Open reads the encrypted vault at path and decrypts it with
// masterPassword.
func Open(path, masterPassword string) (*Vault, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) < saltLen+nonceLen {
		return nil, ErrCorrupt
	}

	salt := raw[:saltLen]
	nonce := raw[saltLen : saltLen+nonceLen]
	ciphertext := raw[saltLen+nonceLen:]

	key := deriveKey(masterPassword, salt)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("vault: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("vault: new gcm: %w", err)
	}

	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		// GCM auth tag mismatch covers both wrong password and tampering.
		return nil, ErrInvalidPassword
	}

	var v Vault
	if err := yaml.Unmarshal(plain, &v); err != nil {
		return nil, fmt.Errorf("vault: unmarshal: %w", err)
	}
	return &v, nil
}

// deriveKey runs the master password through Argon2id to produce a
// 256-bit key. (binary.Bytes at import kept for future version tagging.)
func deriveKey(password string, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, keyLen)
}

// ensure binary import is used if we later version the format.
var _ = binary.LittleEndian

// atomicWrite writes data to path via a temp file + rename so a crash
// never leaves a half-written vault.
func atomicWrite(path string, data []byte, perm os.FileMode) error {
	dir := filepathDir(path)
	f, err := os.CreateTemp(dir, ".vault-tmp-*")
	if err != nil {
		return fmt.Errorf("vault: create temp: %w", err)
	}
	tmp := f.Name()
	cleanup := func() { _ = os.Remove(tmp) }

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		cleanup()
		return fmt.Errorf("vault: write temp: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return fmt.Errorf("vault: close temp: %w", err)
	}
	if err := os.Chmod(tmp, perm); err != nil {
		cleanup()
		return fmt.Errorf("vault: chmod temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		cleanup()
		return fmt.Errorf("vault: rename: %w", err)
	}
	return nil
}

func filepathDir(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			if i == 0 {
				return string(os.PathSeparator)
			}
			return path[:i]
		}
	}
	return "."
}
