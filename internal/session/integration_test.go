package session

import (
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"testing"

	"golang.org/x/crypto/ssh"
	"xcx/internal/vault"
)

// startSSHServer boots an in-process SSH server over TCP that accepts the
// password "goodpw" or any client public key. It returns the server's host
// signer (whose PublicKey is what clients must trust) and its address.
func startSSHServer(t *testing.T) (hostSigner ssh.Signer, addr string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err = ssh.NewSignerFromSigner(priv)
	if err != nil {
		t.Fatal(err)
	}

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if string(pass) == "goodpw" {
				return nil, nil
			}
			return nil, io.ErrUnexpectedEOF
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			return nil, nil // accept any client key for this test
		},
	}
	cfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr = ln.Addr().String()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed
			}
			go serveOne(conn, cfg)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return hostSigner, addr
}

// serveOne completes the handshake; channels are drained and closed.
func serveOne(c net.Conn, cfg *ssh.ServerConfig) {
	defer c.Close()
	sConn, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	defer sConn.Close()
	go ssh.DiscardRequests(reqs)
	for newCh := range chans {
		ch, _, err := newCh.Accept()
		if err != nil {
			continue
		}
		go func(ch ssh.Channel) { _, _ = io.Copy(io.Discard, ch); _ = ch.Close() }(ch)
	}
}

// endToEndDialer dials a real address with ssh.Dial; satisfies Dialer.
type endToEndDialer struct{}

func (endToEndDialer) Dial(network, addr string, config *ssh.ClientConfig) (*ssh.Client, error) {
	return ssh.Dial(network, addr, config)
}

func TestConnect_RealHandshake_Password(t *testing.T) {
	hostSigner, addr := startSSHServer(t)
	ip, port := splitHostPort(addr)

	h := &vault.Host{Name: "srv", Addr: ip, Port: port, User: "u", Auth: vault.Auth{Type: "password", Password: "goodpw"}}
	db := newMemDB()
	// Pre-trust the real server key so the handshake passes.
	if err := db.Add(net.JoinHostPort(ip, strconvI(port)), hostSigner.PublicKey()); err != nil {
		t.Fatal(err)
	}

	sess, err := Connect(h, DialOptions{Dialer: endToEndDialer{}, Verifier: &HostKeyVerifier{DB: db}})
	if err != nil {
		t.Fatalf("Connect real: %v", err)
	}
	defer sess.Close()
	if sess.Client() == nil {
		t.Fatal("nil client after successful connect")
	}
}

func TestConnect_RealHandshake_BadPassword(t *testing.T) {
	hostSigner, addr := startSSHServer(t)
	ip, port := splitHostPort(addr)

	h := &vault.Host{Name: "srv", Addr: ip, Port: port, User: "u", Auth: vault.Auth{Type: "password", Password: "WRONG"}}
	db := newMemDB()
	if err := db.Add(net.JoinHostPort(ip, strconvI(port)), hostSigner.PublicKey()); err != nil {
		t.Fatal(err)
	}

	if _, err := Connect(h, DialOptions{Dialer: endToEndDialer{}, Verifier: &HostKeyVerifier{DB: db}}); err == nil {
		t.Fatal("expected auth failure, got nil")
	}
}

func TestConnect_RealHandshake_UnknownHost(t *testing.T) {
	_, addr := startSSHServer(t)
	ip, port := splitHostPort(addr)
	h := &vault.Host{Name: "srv", Addr: ip, Port: port, User: "u", Auth: vault.Auth{Type: "password", Password: "goodpw"}}

	_, err := Connect(h, DialOptions{Dialer: endToEndDialer{}, Verifier: &HostKeyVerifier{DB: newMemDB()}})
	hke, ok := IsHostKeyError(err)
	if !ok || !hke.Unknown {
		t.Fatalf("expected unknown-host error, got %v", err)
	}
}
