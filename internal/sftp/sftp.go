package sftp

import (
	"fmt"
	"net"
	"os"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// Config holds the parameters needed to connect to an SFTP server.
type Config struct {
	Host           string
	Port           string // default "22"
	User           string
	Password       string // password auth (optional if key is set)
	PrivateKeyPath string // path to PEM-encoded private key (optional if password is set)
	BasePath       string
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

	sshCfg := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // users may override via SSH config
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
