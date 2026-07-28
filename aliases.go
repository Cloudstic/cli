package cloudstic

import (
	"context"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/crypto"
	"github.com/cloudstic/cli/pkg/keychain"
)

// ---------------------------------------------------------------------------
// Re-exported types from internal packages
// ---------------------------------------------------------------------------

// RepoConfig is the repository marker written by "init".
type RepoConfig = core.RepoConfig

// FileMeta represents immutable file metadata, as referenced by
// pkg/source.Source implementations and by result types such as FileMatch
// and LsSnapshotResult.
type FileMeta = core.FileMeta

// SourceInfo describes the origin of a backup snapshot, as referenced by
// pkg/source.Source.Info and by result types such as FileMatch.
type SourceInfo = core.SourceInfo

// FileType is the generic type of a file (file or folder).
type FileType = core.FileType

// FileType values.
const (
	FileTypeFile   = core.FileTypeFile
	FileTypeFolder = core.FileTypeFolder
)

// Snapshot represents a backup checkpoint, as referenced by LsSnapshotResult.
type Snapshot = core.Snapshot

// Reporter defines the interface for progress reporting.
type Reporter = ui.Reporter

// Phase represents an active progress tracking phase.
type Phase = ui.Phase

// KeySlot is re-exported for callers that need to inspect slot metadata.
type KeySlot = keychain.KeySlot

// KMSClient is re-exported for callers that provide KMS credentials.
type KMSClient = crypto.KMSClient

// PasswordProvider supplies a new password when prompted. It is used by
// ChangePassword to obtain the replacement passphrase. Implementations may
// prompt the user interactively, derive a password programmatically, or
// return a static value.
type PasswordProvider interface {
	NewPassword(ctx context.Context) (string, error)
}

// PasswordProviderFunc is a function adapter for PasswordProvider.
// Any func(context.Context) (string, error) can be used as a PasswordProvider:
//
//	client.ChangePassword(ctx, store, creds, cloudstic.PasswordProviderFunc(func(ctx context.Context) (string, error) {
//		return promptUser("New password: ")
//	}))
type PasswordProviderFunc func(ctx context.Context) (string, error)

func (f PasswordProviderFunc) NewPassword(ctx context.Context) (string, error) { return f(ctx) }

// PasswordString is a PasswordProvider that returns a fixed string.
// Use this when the new password is already known at call time:
//
//	client.ChangePassword(ctx, store, creds, cloudstic.PasswordString("my-new-password"))
type PasswordString string

func (p PasswordString) NewPassword(ctx context.Context) (string, error) { return string(p), nil }
