package session

import (
	"fmt"
	"net"
	"strconv"

	"golang.org/x/crypto/ssh"
	"xcx/internal/vault"
)

// Session wraps an established SSH client that can be reused to open
// multiple channels (shell, sftp, ...).
type Session struct {
	Host   *vault.Host
	client *ssh.Client
}

// Client exposes the underlying *ssh.Client for opening channels.
func (s *Session) Client() *ssh.Client { return s.client }

// Close releases the SSH connection.
func (s *Session) Close() error {
	if s.client == nil {
		return nil
	}
	return s.client.Close()
}

// Dialer abstracts ssh.Dial so it can be faked in tests.
type Dialer interface {
	Dial(network, addr string, config *ssh.ClientConfig) (*ssh.Client, error)
}

// sshDialer is the default Dialer backed by golang.org/x/crypto/ssh.Dial.
type sshDialer struct{}

func (sshDialer) Dial(network, addr string, config *ssh.ClientConfig) (*ssh.Client, error) {
	return ssh.Dial(network, addr, config)
}

// DialOptions controls how Connect dials a host.
type DialOptions struct {
	Dialer   Dialer
	Verifier *HostKeyVerifier // required: performs host-key validation
	// Network defaults to "tcp".
	Network string
}

// Connect establishes an SSH connection to host using opts.
// On an unknown or mismatched host key it returns a *HostKeyError; the caller
// may confirm with the user, call opts.Verifier.Trust, and retry.
func Connect(host *vault.Host, opts DialOptions) (*Session, error) {
	if opts.Verifier == nil {
		return nil, fmt.Errorf("session: a HostKeyVerifier is required")
	}
	if opts.Dialer == nil {
		opts.Dialer = sshDialer{}
	}
	if opts.Network == "" {
		opts.Network = "tcp"
	}

	port := host.Port
	if port == 0 {
		port = 22
	}
	addr := net.JoinHostPort(host.Addr, strconv.Itoa(port))

	cfg, err := configForHost(host)
	if err != nil {
		return nil, err
	}
	cfg.HostKeyCallback = opts.Verifier.Callback()

	client, err := opts.Dialer.Dial(opts.Network, addr, cfg)
	if err != nil {
		return nil, err
	}
	return &Session{Host: host, client: client}, nil
}
