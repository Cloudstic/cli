package main

import (
	"fmt"
	"github.com/cloudstic/cli/pkg/config"
	"strings"

	"github.com/cloudstic/cli/pkg/profile"
)

// Store-form domain helpers shared by the dashboard's store and secret backends
// (cmd_tui_store_backend.go, cmd_tui_secret_backend.go). They translate between
// a store's stored URI and the parts a form edits, and classify a store's
// encryption mode.

type tuiStoreConfig struct {
	Type  string
	Value string
}

type tuiStoreEncryptionMode string

const (
	tuiStoreEncryptionNone     tuiStoreEncryptionMode = "none"
	tuiStoreEncryptionPassword tuiStoreEncryptionMode = "password"
	tuiStoreEncryptionPlatform tuiStoreEncryptionMode = "platform"
	tuiStoreEncryptionKMS      tuiStoreEncryptionMode = "kms"
)

var tuiSecretResolver = profileSecretResolver

func newTUIStoreConfig(raw string) tuiStoreConfig {
	parts, err := config.ParseStoreURI(raw)
	if err != nil {
		return tuiStoreConfig{Type: "local"}
	}
	switch parts.Scheme {
	case "local":
		return tuiStoreConfig{Type: "local", Value: parts.Path}
	case "s3", "b2":
		value := parts.Bucket
		if parts.Prefix != "" {
			value += "/" + parts.Prefix
		}
		return tuiStoreConfig{Type: parts.Scheme, Value: value}
	case "sftp":
		target := ""
		if parts.User != "" {
			target += parts.User + "@"
		}
		target += parts.Host
		if parts.Port != "" {
			target += ":" + parts.Port
		}
		target += parts.Path
		return tuiStoreConfig{Type: "sftp", Value: target}
	default:
		return tuiStoreConfig{Type: "local"}
	}
}

func (s tuiStoreConfig) Compose() string {
	value := strings.TrimSpace(s.Value)
	switch s.Type {
	case "local":
		return "local:" + value
	case "s3", "b2":
		return s.Type + ":" + value
	case "sftp":
		if value == "" {
			return ""
		}
		return "sftp://" + value
	default:
		return value
	}
}

func (s tuiStoreConfig) DetailLabel() string {
	switch s.Type {
	case "local":
		return "Path"
	case "sftp":
		return "Target"
	default:
		return "Bucket/Prefix"
	}
}

func (s tuiStoreConfig) Description(editing bool, usedBy int) string {
	if editing {
		if usedBy > 1 {
			return fmt.Sprintf("This store is shared by %d profiles.", usedBy)
		}
		if usedBy == 1 {
			return "This store is currently referenced by 1 profile."
		}
		return "Edit the store settings below."
	}
	switch s.Type {
	case "local":
		return "Store backups in a local filesystem path."
	case "sftp":
		return "Store backups on a remote SFTP server."
	case "b2":
		return "Store backups in a Backblaze B2 bucket."
	default:
		return "Store backups in an S3-compatible bucket."
	}
}

func (s tuiStoreConfig) ExampleText() string {
	switch s.Type {
	case "local":
		return "Example: /Users/me/.cloudstic"
	case "sftp":
		return "Example: backup@host.example.com/backups"
	case "b2":
		return "Example: my-bucket/backups"
	default:
		return "Example: my-bucket/backups"
	}
}

func newTUIStoreEncryptionMode(existing profile.Store) tuiStoreEncryptionMode {
	switch {
	case existing.KMSKeyARN != "":
		return tuiStoreEncryptionKMS
	case existing.EncryptionKeySecret != "":
		return tuiStoreEncryptionPlatform
	case existing.PasswordSecret != "":
		return tuiStoreEncryptionPassword
	default:
		return tuiStoreEncryptionNone
	}
}
