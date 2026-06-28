package session

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"xcx/internal/vault"
)

// --- memHostKeyDB ---------------------------------------------------------

type memHostKeyDB struct {
	keys map[string]ssh.PublicKey
}

func newMemDB() *memHostKeyDB { return &memHostKeyDB{keys: map[string]ssh.PublicKey{}} }

func (m *memHostKeyDB) Lookup(host string) (ssh.PublicKey, error) { return m.keys[host], nil }
func (m *memHostKeyDB) Add(host string, key ssh.PublicKey) error  { m.keys[host] = key; return nil }

// --- helpers --------------------------------------------------------------

func mustKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// --- fake dialer capturing the config ------------------------------------

type fakeDialer struct {
	gotConfig *ssh.ClientConfig
	gotAddr   string
	dialErr   error
}

func (f *fakeDialer) Dial(network, addr string, config *ssh.ClientConfig) (*ssh.Client, error) {
	f.gotAddr = addr
	cfg := *config
	f.gotConfig = &cfg
	if f.dialErr != nil {
		return nil, f.dialErr
	}
	return nil, nil // callers that need a client use the in-process test
}

// --- auth config assembly -------------------------------------------------

func TestConfigForHost_Password(t *testing.T) {
	h := &vault.Host{User: "u", Addr: "1.2.3.4", Port: 22, Auth: vault.Auth{Type: "password", Password: "p"}}
	cfg, err := configForHost(h)
	if err != nil {
		t.Fatalf("configForHost: %v", err)
	}
	if cfg.User != "u" || len(cfg.Auth) != 1 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestConfigForHost_RejectsBadAuth(t *testing.T) {
	cases := []struct {
		name string
		auth vault.Auth
	}{
		{"empty type", vault.Auth{}},
		{"password no secret", vault.Auth{Type: "password"}},
		{"key no path", vault.Auth{Type: "key"}},
		{"bogus type", vault.Auth{Type: "fingerprint"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := configForHost(&vault.Host{User: "u", Auth: c.auth})
			if err == nil {
				t.Fatalf("expected error for %s", c.name)
			}
		})
	}
}

func TestConfigForHost_MissingUser(t *testing.T) {
	_, err := configForHost(&vault.Host{Addr: "h", Auth: vault.Auth{Type: "password", Password: "p"}})
	if err == nil {
		t.Fatal("expected error for missing user")
	}
}

// --- connect address & host-key callback wiring --------------------------

func TestConnect_UsesAddrAndVerifier(t *testing.T) {
	db := newMemDB()
	fd := &fakeDialer{}
	h := &vault.Host{Name: "n", Addr: "10.0.0.5", Port: 2222, User: "u", Auth: vault.Auth{Type: "password", Password: "p"}}

	_, _ = Connect(h, DialOptions{Dialer: fd, Verifier: &HostKeyVerifier{DB: db}})
	if fd.gotAddr != "10.0.0.5:2222" {
		t.Fatalf("dial addr = %q, want 10.0.0.5:2222", fd.gotAddr)
	}
	if fd.gotConfig == nil || fd.gotConfig.HostKeyCallback == nil {
		t.Fatal("HostKeyCallback not wired into config")
	}
}

func TestConnect_DefaultPort22(t *testing.T) {
	db := newMemDB()
	fd := &fakeDialer{}
	h := &vault.Host{Addr: "h", User: "u", Auth: vault.Auth{Type: "password", Password: "p"}} // Port==0
	_, _ = Connect(h, DialOptions{Dialer: fd, Verifier: &HostKeyVerifier{DB: db}})
	if fd.gotAddr != "h:22" {
		t.Fatalf("addr = %q, want h:22", fd.gotAddr)
	}
}

func TestConnect_RequiresVerifier(t *testing.T) {
	_, err := Connect(&vault.Host{Addr: "h", User: "u", Auth: vault.Auth{Type: "password", Password: "p"}}, DialOptions{})
	if err == nil || !strings.Contains(err.Error(), "HostKeyVerifier is required") {
		t.Fatalf("expected verifier-required error, got %v", err)
	}
}

// --- host-key verifier behaviour -----------------------------------------

func TestVerifier_UnknownThenTrust(t *testing.T) {
	db := newMemDB()
	v := &HostKeyVerifier{DB: db}
	cb := v.Callback()
	key := mustKey(t)

	// First contact: Unknown host error.
	err := cb("h:22", nil, key)
	hke, ok := IsHostKeyError(err)
	if !ok || !hke.Unknown {
		t.Fatalf("expected unknown HostKeyError, got %v", err)
	}

	// Trust it, then it should pass.
	if err := v.Trust("h:22", key); err != nil {
		t.Fatal(err)
	}
	if err := cb("h:22", nil, key); err != nil {
		t.Fatalf("after trust, callback failed: %v", err)
	}
}

func TestVerifier_MismatchRejected(t *testing.T) {
	db := newMemDB()
	v := &HostKeyVerifier{DB: db}
	keyA, keyB := mustKey(t), mustKey(t)
	_ = v.Trust("h:22", keyA)

	err := v.Callback()("h:22", nil, keyB)
	hke, ok := IsHostKeyError(err)
	if !ok || hke.Unknown {
		t.Fatalf("expected mismatch (Unknown=false) error, got %v", err)
	}
}

func TestVerifier_TrustOnUnknown(t *testing.T) {
	db := newMemDB()
	v := (&HostKeyVerifier{DB: db}).TrustOnUnknown()
	key := mustKey(t)

	// First contact with trustOnUnknown should be accepted and recorded.
	if err := v.Callback()("h:22", nil, key); err != nil {
		t.Fatalf("trustOnUnknown should accept unknown: %v", err)
	}
	// DB now has the key.
	got, _ := db.Lookup("h:22")
	if got == nil {
		t.Fatal("key not recorded by trustOnUnknown")
	}
	// A different key for the same host is still a mismatch and rejected.
	other := mustKey(t)
	if err := v.Callback()("h:22", nil, other); err == nil {
		t.Fatal("trustOnUnknown must still reject mismatches")
	}
}

func TestIsHostKeyError_NonMatching(t *testing.T) {
	if _, ok := IsHostKeyError(errors.New("other")); ok {
		t.Fatal("plain error should not be a HostKeyError")
	}
}
