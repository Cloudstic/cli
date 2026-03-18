package secretref

import (
	"context"
	"os"
	"path/filepath"

	"github.com/cloudstic/cli/internal/paths"
)

// FileBackend handles file://<path> references.
type FileBackend struct{}

func NewFileBackend() *FileBackend {
	return &FileBackend{}
}

func (b *FileBackend) Resolve(_ context.Context, ref Ref) (string, error) {
	data, err := b.LoadBlob(nil, ref)
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
	if err := saveAtomic(path, data); err != nil {
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
	return data, nil
}

func (b *ConfigTokenBackend) SaveBlob(_ context.Context, ref Ref, data []byte) error {
	path, err := b.resolvePath(ref)
	if err != nil {
		return err
	}
	if err := saveAtomic(path, data); err != nil {
		return errorf(KindBackendUnavailable, ref.Raw, "failed to save managed token file atomically", err)
	}
	return nil
}

func (b *ConfigTokenBackend) DeleteBlob(_ context.Context, ref Ref) error {
	path, err := b.resolvePath(ref)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return errorf(KindBackendUnavailable, ref.Raw, "failed to delete managed token file", err)
	}
	return nil
}

func (b *ConfigTokenBackend) resolvePath(ref Ref) (string, error) {
	// Expected path format: <provider>/<name>
	parts := filepath.SplitList(ref.Path)
	if len(parts) == 0 {
		return "", errorf(KindInvalidRef, ref.Raw, "invalid managed token path; expected <provider>/<name>", nil)
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

	return filepath.Join(tokenDir, filepath.Clean(ref.Path)+".json"), nil
}

// saveAtomic writes data to a temporary file and renames it to path.
// It ensures 0600 permissions on the final file.
func saveAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	if err := os.Chmod(tmp.Name(), 0600); err != nil {
		return err
	}

	if _, err := tmp.Write(data); err != nil {
		return err
	}

	if err := tmp.Sync(); err != nil {
		return err
	}

	if err := tmp.Close(); err != nil {
		return err
	}

	return os.Rename(tmp.Name(), path)
}
