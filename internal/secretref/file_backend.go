package secretref

import (
	"context"
	"fmt"
	"os"
	"os/user"
	pathpkg "path"
	"path/filepath"
	"strings"

	"github.com/cloudstic/cli/internal/logger"
	"github.com/cloudstic/cli/internal/paths"
	"github.com/cloudstic/cli/pkg/crypto"
)

// FileBackend handles file://<path> references.
type FileBackend struct{}

func NewFileBackend() *FileBackend {
	return &FileBackend{}
}

func (b *FileBackend) Resolve(ctx context.Context, ref Ref) (string, error) {
	data, err := b.LoadBlob(ctx, ref)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (b *FileBackend) LoadBlob(_ context.Context, ref Ref) ([]byte, error) {
	path := filepath.Clean(ref.Path)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errorf(KindNotFound, ref.Raw, "file does not exist", err)
		}
		return nil, errorf(KindBackendUnavailable, ref.Raw, "failed to read file", err)
	}
	return data, nil
}

func (b *FileBackend) SaveBlob(_ context.Context, ref Ref, data []byte) error {
	path := filepath.Clean(ref.Path)
	if err := paths.SaveAtomic(path, data); err != nil {
		return errorf(KindBackendUnavailable, ref.Raw, "failed to save file atomically", err)
	}
	return nil
}

func (b *FileBackend) DeleteBlob(_ context.Context, ref Ref) error {
	path := filepath.Clean(ref.Path)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return errorf(KindBackendUnavailable, ref.Raw, "failed to delete file", err)
	}
	return nil
}

func (b *FileBackend) Scheme() string { return "file" }

func (b *FileBackend) DisplayName() string { return "Local file" }

func (b *FileBackend) WriteSupported() bool { return true }

func (b *FileBackend) DefaultRef(name, account string) string {
	_ = account
	return "file:///tmp/cloudstic-secret-" + name
}

func (b *FileBackend) Exists(_ context.Context, ref Ref) (bool, error) {
	_, err := os.Stat(filepath.Clean(ref.Path))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (b *FileBackend) Store(ctx context.Context, ref Ref, value string) error {
	return b.SaveBlob(ctx, ref, []byte(value))
}

// ConfigTokenBackend handles config-token://<provider>/<name> references.
// It stores tokens in the app's managed config directory.
type ConfigTokenBackend struct{}

func NewConfigTokenBackend() *ConfigTokenBackend {
	return &ConfigTokenBackend{}
}

func (b *ConfigTokenBackend) Resolve(ctx context.Context, ref Ref) (string, error) {
	data, err := b.LoadBlob(ctx, ref)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (b *ConfigTokenBackend) LoadBlob(_ context.Context, ref Ref) ([]byte, error) {
	path, err := b.resolvePath(ref)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errorf(KindNotFound, ref.Raw, "managed token file does not exist", err)
		}
		return nil, errorf(KindBackendUnavailable, ref.Raw, "failed to read managed token file", err)
	}

	key, err := b.getEncryptionKey()
	if err != nil {
		return nil, errorf(KindBackendUnavailable, ref.Raw, "failed to derive encryption key", err)
	}

	decrypted, err := crypto.Decrypt(data, key)
	if err != nil {
		// Fallback for unencrypted files (compatibility with existing tokens before RFC 0016)
		if !crypto.IsEncrypted(data) {
			logger.Debugf("decryption failed for %q, but data is not encrypted; falling back to plaintext", ref.Raw)
			return data, nil
		}
		return nil, errorf(KindBackendUnavailable, ref.Raw, "failed to decrypt managed token file", err)
	}
	return decrypted, nil
}

func (b *ConfigTokenBackend) SaveBlob(ctx context.Context, ref Ref, data []byte) error {
	path, err := b.resolvePath(ref)
	if err != nil {
		return err
	}

	key, err := b.getEncryptionKey()
	if err != nil {
		return errorf(KindBackendUnavailable, ref.Raw, "failed to derive encryption key", err)
	}

	encrypted, err := crypto.Encrypt(data, key)
	if err != nil {
		return errorf(KindBackendUnavailable, ref.Raw, "failed to encrypt token", err)
	}

	if err := paths.SaveAtomic(path, encrypted); err != nil {
		return errorf(KindBackendUnavailable, ref.Raw, "failed to save managed token file atomically", err)
	}
	return nil
}

func (b *ConfigTokenBackend) DeleteBlob(ctx context.Context, ref Ref) error {
	path, err := b.resolvePath(ref)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return errorf(KindBackendUnavailable, ref.Raw, "failed to delete managed token file", err)
	}
	return nil
}

func (b *ConfigTokenBackend) Scheme() string { return "config-token" }

func (b *ConfigTokenBackend) DisplayName() string { return "App-managed token (encrypted fallback)" }

func (b *ConfigTokenBackend) WriteSupported() bool { return true }

func (b *ConfigTokenBackend) DefaultRef(name, account string) string {
	provider := account
	if provider == "" {
		provider = "google"
	}
	return "config-token://" + provider + "/" + name
}

func (b *ConfigTokenBackend) Exists(ctx context.Context, ref Ref) (bool, error) {
	path, err := b.resolvePath(ref)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (b *ConfigTokenBackend) Store(ctx context.Context, ref Ref, value string) error {
	return b.SaveBlob(ctx, ref, []byte(value))
}

func (b *ConfigTokenBackend) getEncryptionKey() ([]byte, error) {
	configDir, err := paths.ConfigDir()
	if err != nil {
		return nil, err
	}
	saltFile := filepath.Join(configDir, "auth_salt")
	var salt []byte
	if b, err := os.ReadFile(saltFile); err == nil {
		salt = b
	} else {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read salt file: %w", err)
		}
		s, err := crypto.GenerateKey()
		if err != nil {
			return nil, fmt.Errorf("generate salt: %w", err)
		}
		salt = s
		if err := paths.SaveAtomic(saltFile, salt); err != nil {
			return nil, fmt.Errorf("create salt file: %w", err)
		}
	}

	userID, err := currentUserID()
	if err != nil {
		return nil, fmt.Errorf("determine user identity: %w", err)
	}
	info := fmt.Sprintf("config-token-v1-%s-%s", paths.MachineID(), userID)
	return crypto.DeriveKey(salt, info)
}

func (b *ConfigTokenBackend) resolvePath(ref Ref) (string, error) {
	p := strings.TrimPrefix(ref.Path, "/")
	if p == "" || pathpkg.IsAbs(p) {
		return "", errorf(KindInvalidRef, ref.Raw, "invalid managed token path; expected <provider>/<name>", nil)
	}
	cleaned := pathpkg.Clean(p)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errorf(KindInvalidRef, ref.Raw, "invalid managed token path; expected <provider>/<name>", nil)
	}
	parts := strings.Split(cleaned, "/")
	if len(parts) != 2 {
		return "", errorf(KindInvalidRef, ref.Raw, "invalid managed token path; expected <provider>/<name>", nil)
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", errorf(KindInvalidRef, ref.Raw, "invalid managed token path; expected <provider>/<name>", nil)
		}
	}

	configDir, err := paths.ConfigDir()
	if err != nil {
		return "", errorf(KindBackendUnavailable, ref.Raw, "failed to determine config directory", err)
	}

	// We store tokens in a 'tokens' subdirectory for hygiene.
	tokenDir := filepath.Join(configDir, "tokens")
	if err := os.MkdirAll(tokenDir, 0700); err != nil {
		return "", errorf(KindBackendUnavailable, ref.Raw, "failed to create tokens directory", err)
	}

	resolved := filepath.Join(tokenDir, filepath.FromSlash(cleaned)+".json")
	rel, err := filepath.Rel(tokenDir, resolved)
	if err != nil {
		return "", errorf(KindBackendUnavailable, ref.Raw, "failed to resolve managed token path", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errorf(KindInvalidRef, ref.Raw, "managed token path escapes token directory", nil)
	}
	return resolved, nil
}

func currentUserID() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	if u.Uid != "" {
		return u.Uid, nil
	}
	if u.Username != "" {
		return u.Username, nil
	}
	return "", fmt.Errorf("current user has no stable identifier")
}
