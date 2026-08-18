package nodes

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"golang.org/x/crypto/ssh"
)

// ErrHostKeyMismatch means the server presented a key other than the pinned one.
var ErrHostKeyMismatch = errors.New("ssh host key mismatch")

// errHostKeyCaptured aborts the handshake once HostKeyPrompt has read the
// server's key. It is a sentinel, not a failure: nothing may be executed on a
// host whose fingerprint an admin has not yet confirmed, so the cheapest way
// to stop is to reject the key we just captured. x/crypto/ssh returns the
// callback's error verbatim, wrapped once with %w, so errors.Is recognises it.
var errHostKeyCaptured = errors.New("host key captured")

// SSHCredentials carries bootstrap credentials for exactly one run.
//
// antimage never persists these (spec section 7.1). Storing them would make
// the panel database a root-key vault for the entire fleet, so one panel
// compromise would become total fleet compromise. The type therefore has no
// serialization tags, no store methods, and no migration backing it; Zero()
// wipes the key material as soon as the run finishes.
type SSHCredentials struct {
	Host          string
	Port          int
	User          string
	PrivateKeyPEM []byte
	Passphrase    []byte
}

// Zero overwrites secret material in place and clears identifying fields.
//
// The overwrite has to happen before the slice headers are dropped: nilling
// the fields alone would leave the key bytes sitting in the backing arrays,
// readable by anything that still holds a reference or reads the process's
// memory, until the collector happens to reuse them.
func (c *SSHCredentials) Zero() {
	for i := range c.PrivateKeyPEM {
		c.PrivateKeyPEM[i] = 0
	}
	for i := range c.Passphrase {
		c.Passphrase[i] = 0
	}
	c.PrivateKeyPEM = nil
	c.Passphrase = nil
	c.Host = ""
	c.User = ""
	c.Port = 0
}

func (c SSHCredentials) address() string {
	port := c.Port
	if port == 0 {
		port = 22
	}
	return net.JoinHostPort(c.Host, fmt.Sprintf("%d", port))
}

func (c SSHCredentials) signer() (ssh.Signer, error) {
	if len(c.Passphrase) > 0 {
		return ssh.ParsePrivateKeyWithPassphrase(c.PrivateKeyPEM, c.Passphrase)
	}
	return ssh.ParsePrivateKey(c.PrivateKeyPEM)
}

// verifyHostKey compares a presented fingerprint against the pinned one.
func verifyHostKey(pinned, presented string) error {
	if pinned == presented {
		return nil
	}
	return fmt.Errorf("%w: pinned %s, server presented %s", ErrHostKeyMismatch, pinned, presented)
}

// dialSSH connects and completes the SSH handshake under the caller's context.
//
// ssh.Dial would ignore ctx entirely and its ClientConfig.Timeout bounds only
// the TCP connect, so a host that accepts the connection and then says nothing
// would wedge the calling handler forever. The deadline covers the handshake
// and is cleared once the connection is up, because the bootstrap command
// itself is allowed to take as long as it takes.
func dialSSH(ctx context.Context, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	dialer := net.Dialer{Timeout: cfg.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(cfg.Timeout))
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(sshConn, chans, reqs), nil
}

// HostKeyPrompt connects once purely to read the host key, so the admin can
// confirm its fingerprint before anything is executed. This is
// trust-on-first-use WITH human confirmation, not blind acceptance.
func HostKeyPrompt(ctx context.Context, creds SSHCredentials) (string, error) {
	var fingerprint string

	signer, err := creds.signer()
	if err != nil {
		return "", fmt.Errorf("parse ssh key: %w", err)
	}
	cfg := &ssh.ClientConfig{
		User: creds.User,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			fingerprint = ssh.FingerprintSHA256(key)
			// Capture and stop: nothing is executed on an unconfirmed host.
			return errHostKeyCaptured
		},
		Timeout: 15 * time.Second,
	}

	client, err := dialSSH(ctx, creds.address(), cfg)
	if err == nil {
		_ = client.Close()
		return fingerprint, nil
	}
	if fingerprint != "" && errors.Is(err, errHostKeyCaptured) {
		return fingerprint, nil
	}
	return "", fmt.Errorf("read host key: %w", err)
}

// BootstrapOverSSH runs the bootstrap command against a host whose key
// fingerprint matches pinnedHostKey.
//
// The caller must Zero() the credentials afterwards; this function keeps no
// reference to them once it returns.
func BootstrapOverSSH(ctx context.Context, creds SSHCredentials, pinnedHostKey, command string) (string, error) {
	if pinnedHostKey == "" {
		return "", errors.New("refusing to connect without a pinned host key")
	}
	signer, err := creds.signer()
	if err != nil {
		return "", fmt.Errorf("parse ssh key: %w", err)
	}

	cfg := &ssh.ClientConfig{
		User: creds.User,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			return verifyHostKey(pinnedHostKey, ssh.FingerprintSHA256(key))
		},
		Timeout: 30 * time.Second,
	}

	client, err := dialSSH(ctx, creds.address(), cfg)
	if err != nil {
		return "", fmt.Errorf("ssh dial: %w", err)
	}
	defer func() { _ = client.Close() }()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("ssh session: %w", err)
	}
	defer func() { _ = session.Close() }()

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = session.Signal(ssh.SIGKILL)
		case <-done:
		}
	}()

	output, err := session.CombinedOutput(command)
	close(done)
	if err != nil {
		return string(output), fmt.Errorf("bootstrap command failed: %w", err)
	}
	return string(output), nil
}
