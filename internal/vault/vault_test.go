package vault

import (
	"os"
	"path/filepath"
	"testing"
)

// helperWrite wraps Save so tests don't repeat the master password.
func mustSave(t *testing.T, path, pass string, v *Vault) {
	t.Helper()
	if err := Save(path, pass, v); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

func sampleVault() *Vault {
	return &Vault{
		Groups: []Group{
			{
				Name: "prod",
				Hosts: []Host{
					{
						Name: "web-1", Addr: "10.0.0.1", Port: 22, User: "deploy",
						Auth: Auth{Type: "password", Password: "s3cret"},
					},
					{
						Name: "web-2", Addr: "10.0.0.2", Port: 2222, User: "root",
						Auth: Auth{Type: "key", KeyPath: "/home/u/.ssh/id_ed25519", Passphrase: "pp"},
					},
				},
			},
			{
				Name:  "dev",
				Hosts: []Host{{Name: "local", Addr: "127.0.0.1", Port: 22, User: "u", Auth: Auth{Type: "password", Password: "x"}}},
			},
		},
	}
}

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.bin")
	const pass = "correct horse battery staple"

	mustSave(t, path, pass, sampleVault())

	got, err := Open(path, pass)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(got.Groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(got.Groups))
	}
	if got.Groups[0].Name != "prod" || len(got.Groups[0].Hosts) != 2 {
		t.Fatalf("group[0] unexpected: %+v", got.Groups[0])
	}
	h := got.Groups[0].Hosts[1]
	if h.Port != 2222 || h.Auth.Type != "key" || h.Auth.KeyPath != "/home/u/.ssh/id_ed25519" || h.Auth.Passphrase != "pp" {
		t.Fatalf("host web-2 not preserved: %+v", h)
	}
}

func TestWrongMasterPassword(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.bin")
	mustSave(t, path, "right-password", sampleVault())

	if _, err := Open(path, "wrong-password"); err == nil {
		t.Fatal("expected error for wrong master password, got nil")
	}
}

func TestCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.bin")
	// Truncated: too short to even contain salt+nonce.
	if err := os.WriteFile(path, []byte{1, 2, 3}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, "any"); err == nil {
		t.Fatal("expected error for truncated file, got nil")
	}

	// Long enough but garbage ciphertext: GCM auth tag won't validate.
	garbage := make([]byte, saltLen+nonceLen+32)
	for i := range garbage {
		garbage[i] = byte(i)
	}
	if err := os.WriteFile(path, garbage, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, "any"); err == nil {
		t.Fatal("expected error for garbage ciphertext, got nil")
	}
}

func TestEmptyVault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.bin")
	const pass = "pw"

	mustSave(t, path, pass, &Vault{Groups: nil})

	got, err := Open(path, pass)
	if err != nil {
		t.Fatalf("Open empty vault: %v", err)
	}
	if len(got.Groups) != 0 {
		t.Fatalf("expected empty groups, got %+v", got.Groups)
	}
}

// Two saves with the same password must be able to use different salts
// (random per save) and still decrypt correctly.
func TestSaveRegeneratesSalt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.bin")
	const pass = "pw"
	v := sampleVault()

	mustSave(t, path, pass, v)
	first, _ := os.ReadFile(path)
	mustSave(t, path, pass, v)
	second, _ := os.ReadFile(path)

	if string(first) == string(second) {
		t.Fatal("expected salt/nonce to differ between saves; identical bytes observed")
	}
	got, err := Open(path, pass)
	if err != nil {
		t.Fatalf("Open after re-save: %v", err)
	}
	if len(got.Groups) != 2 {
		t.Fatalf("data lost after re-save; got %d groups", len(got.Groups))
	}
}
