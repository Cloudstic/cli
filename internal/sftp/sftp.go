package sftp

import (
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Config holds the parameters needed to connect to an SFTP server.
type Config struct {
	Host           string
	Port           string // default "22"
	User           string
	Password       string // password auth (optional if key is set)
	PrivateKeyPath string // path to PEM-encoded private key (optional if password is set)
	BasePath       string
	// HostKeyCallback is called during the cryptographic handshake to
	// validate the server's host key. If nil, a default secure callback
	// is used that checks KnownHostsPath or the default system known_hosts.
	HostKeyCallback ssh.HostKeyCallback
	// KnownHostsPath is the path to the known_hosts file. If empty,
	// platform defaults are used (~/.ssh/known_hosts).
	KnownHostsPath string
}

// Dial returns a new SFTP client connected to the server described by cfg.
func Dial(cfg Config) (*sftp.Client, error) {
	port := cfg.Port
	if port == "" {
		port = "22"
	}

	authMethods, err := buildAuthMethods(cfg)
	if err != nil {
		return nil, err
	}

	hostKeyCallback := cfg.HostKeyCallback
	if hostKeyCallback == nil {
		hostKeyCallback, err = defaultHostKeyCallback(cfg.KnownHostsPath)
		if err != nil {
			return nil, fmt.Errorf("host key callback: %w", err)
		}
	}

	sshCfg := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
	}

	conn, err := ssh.Dial("tcp", net.JoinHostPort(cfg.Host, port), sshCfg)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s:%s: %w", cfg.Host, port, err)
	}

	client, err := sftp.NewClient(conn)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("sftp client: %w", err)
	}

	return client, nil
}

func defaultHostKeyCallback(knownHostsPath string) (ssh.HostKeyCallback, error) {
	if knownHostsPath == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			knownHostsPath = filepath.Join(home, ".ssh", "known_hosts")
		}
	}

	if knownHostsPath != "" {
		if _, err := os.Stat(knownHostsPath); err == nil {
			return knownhosts.New(knownHostsPath)
		}
	}

	return nil, fmt.Errorf("no known_hosts file found at %q; use ssh-keyscan to add the host key or specify KnownHostsPath", knownHostsPath)
}

func buildAuthMethods(cfg Config) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	if cfg.PrivateKeyPath != "" {
		pemBytes, err := os.ReadFile(cfg.PrivateKeyPath)
		if err != nil {
			return nil, fmt.Errorf("read private key %s: %w", cfg.PrivateKeyPath, err)
		}
		signer, err := ssh.ParsePrivateKey(pemBytes)
		if err != nil {
			return nil, fmt.Errorf("parse private key %s: %w", cfg.PrivateKeyPath, err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}

	if cfg.Password != "" {
		methods = append(methods, ssh.Password(cfg.Password))
	}

	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		conn, err := net.Dial("unix", sock)
		if err == nil {
			methods = append(methods, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
		}
	}

	if len(methods) == 0 {
		return nil, fmt.Errorf("no SFTP authentication method available")
	}
	return methods, nil
}
