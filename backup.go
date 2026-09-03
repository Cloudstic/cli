package cloudstic

import (
	"context"
	"fmt"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/engine"
	"github.com/cloudstic/cli/internal/repoconfig"
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
		if cfg.Version >= core.MaxInPlaceUpgradeFormat {
			c.noteRepoFormat(int64(cfg.Version))
			return nil
		}
	}

	if err := UpgradeRepoFormat(ctx, c.base, core.MaxInPlaceUpgradeFormat, c.encryptionKey); err != nil {
		return err
	}
	c.noteRepoFormat(int64(core.MaxInPlaceUpgradeFormat))
	return nil
}

// noteRepoFormat records that the repository's on-disk format is at least v.
// The in-process view only ever rises, matching UpgradeRepoFormat's floor on
// disk — which is what keeps a raise to this build's *default* from demoting
// the view of a repository recorded at a higher format. Storing the default
// unconditionally here is how a v3 repository's client once flipped back to
// writing v2 structures mid-command.
func (c *Client) noteRepoFormat(v int64) {
	for {
		cur := c.repoFormat.Load()
		if cur >= v || c.repoFormat.CompareAndSwap(cur, v) {
			return
		}
	}
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
	c.ensureRepoID(ctx)
}

// ensureRepoID gives a repository an identifier if it has none, so that one
// written before RepoConfig.ID existed — or one whose marker an older build
// rewrote, dropping the field — acquires one by being used rather than by the
// operator knowing to re-run `init`.
//
// It runs on mutations only, following the same rule as the format stamp: a
// read that quietly rewrote the marker would be a surprising side effect of
// listing snapshots, and would fail against the read-only credentials that
// reading a repository is meant to need.
//
// Best-effort, deliberately. An identifier only makes `copy` cheaper and more
// precise — provenance falls back to matching on the source snapshot ref
// without one — so it is never worth failing a completed mutation over. The
// format stamp is fatal before a backup for a reason that does not apply here:
// it decides how bytes are written, and this decides nothing.
//
// Two machines mutating one repository at the same moment can each mint an
// identifier, and the later write wins. The cost is bounded and self-correcting:
// snapshots copied under the losing identifier recorded it, a later copy
// computes the winner, CopyProvenance.Matches declines to match two known and
// different repositories, and that history is imported once more before
// settling. That is the same failure this design already accepts from an older
// build stripping the field, which is why matching treats an absent identifier
// as unknown rather than as a distinguishing value.
func (c *Client) ensureRepoID(ctx context.Context) {
	if c.base == nil {
		return
	}
	if cached := c.repoIDCache.Load(); cached != nil && *cached != "" {
		return
	}

	// Re-read rather than trusting the cache's empty value. The marker is read
	// immediately before it is written, so a peer that assigned an identifier
	// since this client opened is seen and left alone.
	raw, err := fetchRepoConfigBytes(ctx, c.base)
	if err != nil {
		c.log.Debugf("could not read repository config to assign an id: %v", err)
		return
	}
	if raw == nil {
		return // not initialized; init assigns the identifier
	}

	// Assigning an identifier must change nothing else about the repository,
	// and for a marker that is plaintext on an encrypted repository it cannot:
	// writing it back would seal it, and a sealed marker cannot be read by
	// builds that predate sealing. That is a version-gated decision about who
	// can still open the repository, which belongs to the format stamp and not
	// to this. Such a marker acquires an identifier once something else seals
	// it; until then `copy` matches provenance on the snapshot ref alone, which
	// is the documented fallback.
	if !repoconfig.IsSealed(raw) && len(c.encryptionKey) > 0 {
		cfg, decErr := repoconfig.Decode(raw, nil)
		if decErr == nil && cfg.Encrypted {
			c.log.Debugf("leaving the plaintext marker of an encrypted repository alone rather than sealing it to assign an id")
			return
		}
	}

	cfg, err := repoconfig.Decode(raw, c.encryptionKey)
	if err != nil {
		c.log.Debugf("could not decode repository config to assign an id: %v", err)
		return
	}
	if cfg.ID != "" {
		c.repoIDCache.Store(&cfg.ID)
		return
	}

	id, err := core.NewRepoID()
	if err != nil {
		c.log.Debugf("could not generate a repository id: %v", err)
		return
	}
	cfg.ID = id
	if err := putRepoConfig(ctx, c.base, *cfg, c.encryptionKey); err != nil {
		c.log.Debugf("could not assign a repository id: %v", err)
		return
	}
	c.repoIDCache.Store(&id)
	c.log.Debugf("assigned repository id %s", id)
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

	// Backup writes through its own meter so the run's raw byte count is
	// separable from the client-wide stored-bytes total.
	deps := c.engineDeps()
	deps.Store = rawMeter

	mgr := engine.NewBackupManager(deps, src, opts...)
	result, err := mgr.Run(ctx)
	if err != nil {
		return nil, err
	}

	// Backup stamps the format before writing rather than after, so it does not
	// pass through stampWriteFormat and has to ask for the identifier itself.
	// A dry run wrote nothing and must not start here.
	if !result.DryRun {
		c.ensureRepoID(ctx)
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
