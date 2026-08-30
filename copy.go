package cloudstic

import (
	"context"
	"fmt"

	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/engine"
)

// ---------------------------------------------------------------------------
// Copy
// ---------------------------------------------------------------------------

// CopyOption configures a copy run.
type CopyOption = engine.CopyOption

// CopyResult reports what a copy run did.
type CopyResult = engine.CopyResult

// CopiedSnapshot records one snapshot transferred into the destination.
type CopiedSnapshot = engine.CopiedSnapshot

// SkippedSnapshot records one snapshot the destination already had.
type SkippedSnapshot = engine.SkippedSnapshot

var (
	WithCopySnapshotIDs   = engine.WithCopySnapshotIDs
	WithCopyFilterSource  = engine.WithCopyFilterSource
	WithCopyFilterPath    = engine.WithCopyFilterPath
	WithCopyFilterAccount = engine.WithCopyFilterAccount
	WithCopyFilterTag     = engine.WithCopyFilterTag
	WithCopySince         = engine.WithCopySince
	WithCopyDryRun        = engine.WithCopyDryRun
	WithCopyAllowCopied   = engine.WithCopyAllowCopied
)

// CopyFrom transfers snapshots from src into this repository.
//
// It is a method on the *destination* — the client that will be written to —
// and takes the source as another client rather than as a bare store. That is
// deliberate on three counts:
//
//   - The store decorator chain is never composed by a caller. Its order is a
//     correctness and security invariant, and a chain assembled without a pack
//     index key yields a repository whose index is plaintext with no error at
//     any layer. A *Client has already had that chain built for it.
//   - Source credentials stay out of the copy option surface: the source client
//     was constructed with WithKeychain like any other.
//   - The source is format-gated for free, because NewClient reads and checks
//     its repository marker.
//
// Nothing is written to the source. A copy needs only read access there, which
// is what makes read-only source credentials a supported configuration.
//
// The two repositories need not share a format. A copy reads whichever form the
// source stores its entries in — standalone filemeta and content objects, or
// the leaf payloads of format v3 (RFC 0026) — and writes whichever the
// destination records, so copying into a repository created with format 3 is
// how a packfile-era repository is migrated. The destination's recorded format
// decides what is written for every entry, so a copy never leaves it holding a
// mixture.
//
// Copying is expensive compared with an incremental backup: every object in the
// selected snapshots is read and decrypted through the source, then re-encrypted
// and written through the destination, because object names are derived from
// each repository's own key and so cannot be carried across. Data the
// destination already holds is still recognised and skipped.
func (c *Client) CopyFrom(ctx context.Context, src *Client, opts ...CopyOption) (*CopyResult, error) {
	if src == nil {
		return nil, fmt.Errorf("copy: no source repository given")
	}
	if src == c {
		return nil, fmt.Errorf("copy: source and destination are the same client")
	}

	srcID, err := src.repoID(ctx)
	if err != nil {
		return nil, fmt.Errorf("read source repository config: %w", err)
	}
	dstID, err := c.repoID(ctx)
	if err != nil {
		return nil, fmt.Errorf("read destination repository config: %w", err)
	}

	same, err := sameRepository(ctx, src, c, srcID, dstID)
	if err != nil {
		return nil, err
	}
	if same {
		return nil, fmt.Errorf(
			"copy: source and destination are the same repository -- copying one into itself " +
				"would duplicate every snapshot in its history",
		)
	}

	mgr := engine.NewCopyManager(
		c.engineDeps(),
		engine.CopySide{
			Store:     src.store,
			RepoID:    srcID,
			FormatV3:  src.formatV3(),
			BlobStore: src.storedMeter,
			Sealer:    src.memberSealer,
		},
		dstID,
	)

	// Bytes written are metered as they land in the backend — after compression
	// and encryption — which is the number that matches what the destination is
	// billed for. Bytes read are counted by the manager as plaintext, since that
	// is the volume it actually had to materialise.
	c.storedMeter.Reset()
	result, err := mgr.Run(ctx, opts...)
	if err != nil {
		return nil, err
	}
	result.BytesWritten = c.storedMeter.BytesWritten()

	if !result.DryRun && len(result.Copied) > 0 {
		c.stampWriteFormat(ctx)
	}
	return result, nil
}

// sameRepository reports whether two clients address one repository.
//
// Copying a repository into itself is not merely pointless: every source
// snapshot is rewritten as a *new* snapshot object carrying provenance, so the
// history doubles. No data is duplicated — every object re-addresses to the ref
// it already has — but retention grouping, `latest` and every count are wrong
// afterwards, and undoing it means forgetting half the snapshots by hand.
//
// Repository identifiers settle the question when both repositories have one.
// When either does not — a repository written before the marker carried an id,
// or one whose marker an older build has since rewritten — the two stores are
// probed instead: a uniquely named object is written to the destination and
// looked for through the source.
//
// The probe is exact where every cheaper test is not. Comparing store URIs
// misses "local:/srv/repo" against "local:/srv/repo/.", a symlink, a bind mount
// or one bucket reachable under two endpoints. Comparing the stored markers
// misses nothing structurally but collides in practice: strip the identifier
// and two repositories created in the same second are byte-identical, so it
// refuses legitimate copies between distinct id-less repositories.
//
// It writes only to the destination, which this call is about to write a great
// deal more into, and removes what it wrote. The source is only ever read, so
// read-only source credentials remain sufficient.
func sameRepository(ctx context.Context, src, dst *Client, srcID, dstID string) (bool, error) {
	if srcID != "" && dstID != "" {
		return srcID == dstID, nil
	}

	nonce, err := core.NewRepoID()
	if err != nil {
		return false, err
	}
	// Under index/, which is never packed and never content-addressed, so the
	// probe cannot be mistaken for repository data if a crash strands it.
	key := "index/copy-probe/" + nonce

	if err := dst.base.Put(ctx, key, []byte(nonce)); err != nil {
		return false, fmt.Errorf("probe destination repository: %w", err)
	}
	defer func() { _ = dst.base.Delete(ctx, key) }()

	seen, err := src.base.Exists(ctx, key)
	if err != nil {
		return false, fmt.Errorf("probe source repository: %w", err)
	}
	return seen, nil
}

// repoID returns this repository's identifier, or "" for a repository written
// before the marker carried one — including one whose marker an older build
// has since rewritten, dropping the field.
//
// A cached non-empty value is authoritative: an identifier is assigned once and
// never changed, so it cannot go stale. An empty one is re-read, because a peer
// may have assigned one since this client opened, and copying against a stale
// "no identifier" would fall back to probing for no reason.
func (c *Client) repoID(ctx context.Context) (string, error) {
	if cached := c.repoIDCache.Load(); cached != nil && *cached != "" {
		return *cached, nil
	}
	cfg, _, err := c.openRepoConfig(ctx, c.base)
	if err != nil {
		return "", err
	}
	if cfg == nil {
		return "", fmt.Errorf("repository not initialized")
	}
	c.repoIDCache.Store(&cfg.ID)
	return cfg.ID, nil
}
