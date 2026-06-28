// Package session manages SSH connections: authentication, host key
// verification, and lifecycle of the underlying *ssh.Client.
package session

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
	"xcx/internal/vault"
)

// buildAuth translates a vault.Auth into the corresponding ssh.AuthMethod.
func buildAuth(a vault.Auth) (ssh.AuthMethod, error) {
	switch a.Type {
	case "password":
		if a.Password == "" {
			return nil, fmt.Errorf("password auth requires a password")
		}
		return ssh.Password(a.Password), nil
	case "key":
		if a.KeyPath == "" {
			return nil, fmt.Errorf("key auth requires key_path")
		}
		return keyAuth(a.KeyPath, a.Passphrase)
	default:
		return nil, fmt.Errorf("unsupported auth type %q (want \"password\" or \"key\")", a.Type)
	}
}

func keyAuth(path, passphrase string) (ssh.AuthMethod, error) {
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read key %s: %w", path, err)
	}
	var signer ssh.Signer
	if passphrase == "" {
		signer, err = ssh.ParsePrivateKey(pemBytes)
	} else {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(pemBytes, []byte(passphrase))
	}
	if err != nil {
		// Don't echo key material; surface a generic-ish message.
		if strings.Contains(err.Error(), "password protected") || strings.Contains(err.Error(), "passphrase") {
			return nil, fmt.Errorf("private key needs a passphrase (or it was wrong)")
		}
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	return ssh.PublicKeys(signer), nil
}

// configForHost assembles an ssh.ClientConfig (without HostKeyCallback, which
// the caller attaches) from a vault.Host.
func configForHost(h *vault.Host) (*ssh.ClientConfig, error) {
	method, err := buildAuth(h.Auth)
	if err != nil {
		return nil, err
	}
	user := h.User
	if user == "" {
		return nil, fmt.Errorf("host %q has no user", h.Name)
	}
	port := h.Port
	if port == 0 {
		port = 22
	}
	_ = port // port is used by Dial, not ClientConfig
	return &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{method},
		HostKeyCallback: nil, // attached by Connect via the verifier
	}, nil
}
