package cloudstic

import (
	"context"
	"fmt"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/engine"
	"github.com/cloudstic/cli/internal/storelayer"
	"github.com/cloudstic/cli/pkg/secretref"
	"github.com/cloudstic/cli/pkg/source"
)

// ---------------------------------------------------------------------------
// Backup
// ---------------------------------------------------------------------------

type BackupOption = engine.BackupOption
type BackupResult = engine.RunResult

var (
	WithBackupDryRun        = engine.WithBackupDryRun
	WithIgnoreEmptySnapshot = engine.WithIgnoreEmptySnapshot
	WithTags                = engine.WithTags
	WithGenerator           = engine.WithGenerator
	WithMeta                = engine.WithMeta
	WithExcludeHash         = engine.WithExcludeHash
)

// noRepoConfig marks Client.openCfg as spent. A plain nil cannot: nil is also
// what the field holds for an uninitialized repository, and those two states
// have to stay distinguishable.
var noRepoConfig RepoConfig

// raiseRepoFormat stamps the on-disk format and updates the in-process view in
// lockstep, so format-dependent write policy — framing — follows immediately.
// It is the only writer of c.repoFormat.
//
// Raising is a write, so it obeys the "writes stamp, reads do not" rule: a read
// that silently changed the repository would be a surprising side effect of an
// innocuous command, and would lock out a machine that was only listing
// snapshots. A newer build having *written* here is a real signal; a newer
// build having *looked* is not.
func (c *Client) raiseRepoFormat(ctx context.Context) error {
	if c.base == nil {
		return nil
	}

	// The first raise can answer from the config NewClient just read, instead
	// of fetching "config" a second time within the same command — two round
	// trips to read one small immutable object, back to back, which is
	// plainly visible on a high-latency backend.
	//
	// Only the first. The cached copy is consumed here and every later raise
	// re-reads, because "already current" then stops being a safe assumption:
	// a long-lived client outlives the moment it was opened, and the on-disk
	// version is what other machines act on. Skipping the write on a stale
	// in-process belief would leave a repository unstamped after a real
	// mutation — see TestDryRunsDoNotStampTheFormat.
	if cfg := c.openCfg.Swap(&noRepoConfig); cfg != nil && cfg != &noRepoConfig {
		if cfg.Version >= core.RepoFormatVersion {
			c.repoFormat.Store(int64(core.RepoFormatVersion))
			return nil
		}
	}

	if err := UpgradeRepoFormat(ctx, c.base, core.RepoFormatVersion, c.encryptionKey); err != nil {
		return err
	}
	c.repoFormat.Store(int64(core.RepoFormatVersion))
	return nil
}

// stampWriteFormat raises the format after a successful mutation, best-effort:
// the data is already written and the next mutation stamps again, so a failure
// is logged rather than surfaced.
//
// This is right for prune and forget. What they write through the compression
// layer is JSON — snapshot and index manifests — which always compresses, so an
// unframed one is decoded correctly by the legacy read path and nothing is lost
// by stamping afterwards. Backup is the exception: it stamps *before* writing
// (see its comment), because it stores user file content, the one thing an
// unframed write corrupts permanently.
func (c *Client) stampWriteFormat(ctx context.Context) {
	if err := c.raiseRepoFormat(ctx); err != nil {
		c.log.Debugf("could not stamp repository format: %v", err)
	}
}

func (c *Client) Backup(ctx context.Context, src source.Source, opts ...BackupOption) (*BackupResult, error) {
	// Raise the format before writing anything, and fail if it cannot be raised.
	//
	// Backup stores user file content, which may be incompressible and may begin
	// with a magic header — the combination the frame exists for. An object this
	// build writes unframed is unframed permanently: content-addressed objects
	// are never rewritten, so a later framed backup skips it on the Exists check
	// and nothing repairs it. Stamping afterwards, as prune and forget do, would
	// mean every repository below the framing format lost its already-compressed
	// files on the first backup after upgrading. Raising first makes framing
	// (which follows the format) already on by the time the first object is
	// written; continuing on a raise error would write exactly those unframed
	// objects, so the error is fatal.
	if err := c.raiseRepoFormat(ctx); err != nil {
		return nil, fmt.Errorf("raise repository format before writing: %w", err)
	}

	rawMeter := storelayer.NewMeteredStore(c.store)
	c.storedMeter.Reset()

	mgr := engine.NewBackupManager(src, rawMeter, c.reporter, c.hmacKey, c.logWriter, opts...)
	result, err := mgr.Run(ctx)
	if err != nil {
		return nil, err
	}

	result.BytesAddedRaw = rawMeter.BytesWritten()
	result.BytesAddedStored = c.storedMeter.BytesWritten()
	return result, nil
}

// SecretRefError reports a malformed scheme://path secret reference (e.g. in
// one of a profile's *_secret fields). Use errors.As to inspect it and Kind
// to branch on the failure mode.
type SecretRefError = secretref.Error

// SecretRefErrorKind categorizes a SecretRefError.
type SecretRefErrorKind = secretref.ErrorKind

const (
	SecretRefInvalid            = secretref.KindInvalidRef
	SecretRefNotFound           = secretref.KindNotFound
	SecretRefBackendUnavailable = secretref.KindBackendUnavailable
)

// SecretResolver resolves a scheme://path secret reference to its value, as
// accepted by pkg/source/onedrive.WithResolver and pkg/source/gdrive.WithResolver.
type SecretResolver = secretref.Resolver

// SecretRef is a parsed scheme://path secret reference.
type SecretRef = secretref.Ref

// WritableSecretBackend is a secret backend that supports writing new values,
// as returned by SecretResolver.WritableBackends.
type WritableSecretBackend = secretref.WritableBackend
