package store

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// Packfiles carry a trailing, self-describing index so that the location of
// every packed object can be recovered from the packfile itself. Without it,
// `index/packs` is the only record of where a packed object lives and its loss
// is unrecoverable: the bytes remain intact but nothing can locate them.
//
// The layout is:
//
//	┌──────────────────────────────────────────────┐
//	│ object bytes (concatenated)                   │
//	├──────────────────────────────────────────────┤
//	│ footer payload (JSON)                         │
//	├──────────────────────────────────────────────┤
//	│ footer length   uint32 big-endian   4 bytes   │
//	│ format version  uint8               1 byte    │
//	│ magic "CSPACK"                      6 bytes   │
//	└──────────────────────────────────────────────┘
//
// The trailer is fixed width, so a reader starts at the end of the object,
// validates the magic, and works backwards. Object bytes keep their position
// and meaning, so a reader that already knows an offset and length is
// unaffected by the footer's presence — which is what keeps packs written by a
// newer client readable by an older one.
//
// See RFC 0018.
const (
	packMagic         = "CSPACK"
	packFooterVersion = 1
	packTrailerLen    = 4 + 1 + len(packMagic)
)

// errNoPackFooter reports a packfile written before footers existed. It is an
// expected condition on repositories created by an older client, not a fault.
var errNoPackFooter = errors.New("packfile has no footer")

type packFooterEntry struct {
	Key    string `json:"k"`
	Offset int64  `json:"o"`
	Length int64  `json:"l"`
}

type packFooter struct {
	Version int               `json:"v"`
	Entries []packFooterEntry `json:"entries"`
}

// packFooterInfo is a decoded footer plus the size of the object region that
// precedes it, which callers need to reason about pack waste without counting
// the footer itself as garbage.
type packFooterInfo struct {
	entries         map[string]PackEntry
	objectRegionLen int64
}

// buildPackFooter serialises entries into a footer payload plus trailer.
// Entries are sorted by key so that identical object sets produce byte-identical
// packfiles, preserving the content-addressed packfile name.
func buildPackFooter(entries map[string]PackEntry) ([]byte, error) {
	footer := packFooter{
		Version: packFooterVersion,
		Entries: make([]packFooterEntry, 0, len(entries)),
	}
	for key, entry := range entries {
		footer.Entries = append(footer.Entries, packFooterEntry{
			Key:    key,
			Offset: entry.Offset,
			Length: entry.Length,
		})
	}
	sort.Slice(footer.Entries, func(i, j int) bool {
		return footer.Entries[i].Key < footer.Entries[j].Key
	})

	payload, err := json.Marshal(&footer)
	if err != nil {
		return nil, fmt.Errorf("marshal pack footer: %w", err)
	}
	if uint64(len(payload)) > uint64(^uint32(0)) {
		return nil, fmt.Errorf("pack footer too large: %d bytes", len(payload))
	}

	out := make([]byte, 0, len(payload)+packTrailerLen)
	out = append(out, payload...)
	out = binary.BigEndian.AppendUint32(out, uint32(len(payload)))
	out = append(out, byte(packFooterVersion))
	out = append(out, packMagic...)
	return out, nil
}

// parsePackTrailer validates a fixed-width trailer and returns the footer
// payload length it declares.
func parsePackTrailer(trailer []byte) (int64, error) {
	if len(trailer) != packTrailerLen {
		return 0, fmt.Errorf("pack trailer must be %d bytes, got %d", packTrailerLen, len(trailer))
	}
	if string(trailer[packTrailerLen-len(packMagic):]) != packMagic {
		return 0, errNoPackFooter
	}
	if version := trailer[4]; version != packFooterVersion {
		return 0, fmt.Errorf("unsupported pack footer version %d", version)
	}
	return int64(binary.BigEndian.Uint32(trailer[:4])), nil
}

// decodePackFooter reads the footer from a complete packfile.
func decodePackFooter(packRef string, packData []byte) (*packFooterInfo, error) {
	if len(packData) < packTrailerLen {
		return nil, errNoPackFooter
	}
	payloadLen, err := parsePackTrailer(packData[len(packData)-packTrailerLen:])
	if err != nil {
		return nil, err
	}

	footerStart := int64(len(packData)) - int64(packTrailerLen) - payloadLen
	if footerStart < 0 {
		return nil, fmt.Errorf("pack %s declares a %d byte footer but is only %d bytes", packRef, payloadLen, len(packData))
	}

	return decodePackFooterPayload(packRef, packData[footerStart:int64(len(packData))-int64(packTrailerLen)], footerStart)
}

func decodePackFooterPayload(packRef string, payload []byte, objectRegionLen int64) (*packFooterInfo, error) {
	var footer packFooter
	if err := json.Unmarshal(payload, &footer); err != nil {
		return nil, fmt.Errorf("unmarshal footer of pack %s: %w", packRef, err)
	}
	if footer.Version != packFooterVersion {
		return nil, fmt.Errorf("unsupported pack footer version %d in %s", footer.Version, packRef)
	}

	entries := make(map[string]PackEntry, len(footer.Entries))
	for _, e := range footer.Entries {
		if e.Offset < 0 || e.Length < 0 || e.Offset+e.Length > objectRegionLen {
			return nil, fmt.Errorf("footer of pack %s places %s outside the object region", packRef, e.Key)
		}
		entries[e.Key] = PackEntry{PackRef: packRef, Offset: e.Offset, Length: e.Length}
	}
	return &packFooterInfo{entries: entries, objectRegionLen: objectRegionLen}, nil
}

// readPackFooter fetches just the footer of a packfile, using a ranged read
// when the backend supports one so that recovery does not have to transfer
// whole 8 MB packs. Returns errNoPackFooter for packs written before footers
// existed.
//
// It must not take s.mu: callers may already hold it.
func (s *PackStore) readPackFooter(ctx context.Context, packRef string) (*packFooterInfo, error) {
	if packData, ok := s.packCache.Get(packRef); ok {
		return decodePackFooter(packRef, packData)
	}

	ranger, ok := s.ObjectStore.(RangeGetter)
	if !ok {
		// No ranged read available; fall back to transferring the whole pack.
		packData, err := s.ObjectStore.Get(ctx, packRef)
		if err != nil {
			return nil, fmt.Errorf("read pack %s: %w", packRef, err)
		}
		return decodePackFooter(packRef, packData)
	}

	size, err := s.ObjectStore.Size(ctx, packRef)
	if err != nil {
		return nil, fmt.Errorf("size of pack %s: %w", packRef, err)
	}
	if size < int64(packTrailerLen) {
		return nil, errNoPackFooter
	}

	trailer, err := ranger.GetRange(ctx, packRef, size-int64(packTrailerLen), int64(packTrailerLen))
	if err != nil {
		return nil, fmt.Errorf("read trailer of pack %s: %w", packRef, err)
	}
	payloadLen, err := parsePackTrailer(trailer)
	if err != nil {
		return nil, err
	}

	objectRegionLen := size - int64(packTrailerLen) - payloadLen
	if objectRegionLen < 0 {
		return nil, fmt.Errorf("pack %s declares a %d byte footer but is only %d bytes", packRef, payloadLen, size)
	}

	payload, err := ranger.GetRange(ctx, packRef, objectRegionLen, payloadLen)
	if err != nil {
		return nil, fmt.Errorf("read footer of pack %s: %w", packRef, err)
	}
	return decodePackFooterPayload(packRef, payload, objectRegionLen)
}

// RebuildCatalog reconstructs the pack catalog by reading every packfile's
// footer, and merges the result into the in-memory catalog.
//
// This is what demotes index/packs from a single point of failure to a cache:
// entries recovered here are authoritative, because they come from inside the
// immutable, content-addressed packfile they describe. Packs written before
// footers existed contribute nothing and are reported as unrecoverable, since
// their offsets genuinely exist nowhere but the catalog.
//
// Existing in-memory entries win over recovered ones. Both locate byte-identical
// content, so the merge is idempotent and order-independent.
func (s *PackStore) RebuildCatalog(ctx context.Context) (recovered int, footerless int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rebuildCatalogLocked(ctx)
}

// rebuildCatalogLocked is RebuildCatalog's body. mu must be held.
func (s *PackStore) rebuildCatalogLocked(ctx context.Context) (recovered int, footerless int, err error) {
	packRefs, err := s.ObjectStore.List(ctx, packPrefix)
	if err != nil {
		return 0, 0, fmt.Errorf("list packs: %w", err)
	}
	return s.rebuildFromPacksLocked(ctx, packRefs)
}

// rebuildFromPacksLocked merges the footers of the given packs into the
// catalog. mu must be held.
func (s *PackStore) rebuildFromPacksLocked(ctx context.Context, packRefs []string) (recovered int, footerless int, err error) {
	for _, packRef := range packRefs {
		info, err := s.readPackFooter(ctx, packRef)
		if errors.Is(err, errNoPackFooter) {
			footerless++
			debugf("rebuild: pack %s has no footer, relying on the catalog", packRef)
			continue
		}
		if err != nil {
			return recovered, footerless, err
		}

		for key, entry := range info.entries {
			if _, exists := s.catalog[key]; !exists {
				s.catalog[key] = entry
				recovered++
				s.catalogDirty = true
			}
		}
	}

	debugf("rebuild: recovered %d entries, %d packs without footers", recovered, footerless)
	return recovered, footerless, nil
}

// healMissingCatalogLocked reconstructs the catalog when index/packs is absent
// but packfiles exist — the signature of a lost catalog rather than a fresh
// repository. mu must be held.
func (s *PackStore) healMissingCatalogLocked(ctx context.Context) error {
	packRefs, err := s.ObjectStore.List(ctx, packPrefix)
	if err != nil {
		return fmt.Errorf("list packs while checking for a lost catalog: %w", err)
	}
	if len(packRefs) == 0 {
		return nil // Genuinely a fresh repository.
	}

	debugf("catalog %s is missing but %d packs exist; rebuilding from footers", indexPacksKey, len(packRefs))
	recovered, footerless, err := s.rebuildFromPacksLocked(ctx, packRefs)
	if err != nil {
		return fmt.Errorf("rebuild catalog from pack footers: %w", err)
	}
	if footerless > 0 {
		return fmt.Errorf(
			"%s is missing and %d of %d packfiles predate self-describing footers, "+
				"so the objects they contain cannot be located; recovered %d entries from the remaining packs",
			indexPacksKey, footerless, len(packRefs), recovered,
		)
	}
	debugf("rebuilt catalog with %d entries from pack footers", recovered)
	return nil
}
