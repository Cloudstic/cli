package storelayer

// A local-disk tier for the repository's large immutable objects.
//
// # Why this layer, and why here
//
// Both repository formats aggregate: v2 bundles small objects into packfiles,
// v3 packs file bodies into blobs. Both then read those aggregates with byte
// ranges, and both re-read the same aggregate many times over one operation.
// Measured on a 20,000-file source tree with five snapshots, a full restore
// against a local store:
//
//	format   ranged reads   distinct objects   amplification
//	v2              4,576            19 packs           241x
//	v3              1,639            16 blobs           102x
//
// That is not a property of either layout. It is the cost of holding the
// working set in memory: the in-memory tier is bounded at eight packs because
// residency must not track repository size (+347 MB on check, +580 MB on
// prune when a larger memory budget was measured), and once an object is
// evicted before it has paid for itself, reads degrade to one request per
// object. The bound is set by how much memory a process may hold, not by
// anything about the data — which is why moving the tier to disk removes the
// amplification without reintroducing the objection.
//
// Placing it directly above the backend is what makes one mechanism serve both
// formats. Everything the repository stores passes here in the form it is
// stored in, so the layer needs no knowledge of which format wrote it: it
// caches every immutable object and declines the three mutable ones. v2's
// packs and v3's blobs are simply the largest things it sees.
//
// # Why it is safe to keep on disk
//
// In an encrypted repository these objects hold ciphertext. PackStore and the
// blob writer both sit below EncryptedStore and seal their members
// individually, so what lands in the cache directory was already sealed before
// it was aggregated: the directory discloses object sizes and count, not
// content. Verified rather than assumed — a distinctive plaintext string from
// a source tree appears in no repository file, and a pack measures 8.00 bits
// of entropy per byte.
//
// In a repository with no encryption the cache holds exactly what the store
// holds, which is plaintext. That is not a new exposure — the store has the
// same bytes — but it does mean the directory is as sensitive as the
// repository, which is why it is created 0700 whatever the repository is.
//
// These objects are also immutable, so a cached entry can never be stale and
// needs no invalidation — the hard half of most caches, free here.
//
// # What it does not do
//
// It does not detect corruption at the backend. An entry served from disk is
// not re-read remotely, so an object that rots in the store after being cached
// reads as healthy. `check` is the operation whose whole purpose is to notice
// that, which is why it opens its client with the cache disabled rather than
// trusting a local copy of the thing it is verifying.
//
// It does not make the working set smaller, and it is not a substitute for a
// layout that reads less. A repository whose aggregates exceed the disk budget
// thrashes here exactly as it does in memory, just later and more cheaply.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudstic/cli/pkg/store"
)

const (
	// DiskCacheBudget is the default number of bytes the disk tier holds.
	// Unlike the memory budget this may be generous — it costs disk, three
	// orders of magnitude cheaper than the resident memory the in-memory
	// budget rations — but it is still a bound rather than "as much as it
	// takes", so a cache directory cannot grow without limit on a machine that
	// backs up many repositories.
	//
	// It is the default rather than the limit: how much disk a machine may
	// spend on this is a property of the machine and not of this program, so
	// CLOUDSTIC_OBJECT_CACHE_BYTES moves it. Having a bound at all is not
	// negotiable, which is why no value of that variable removes one.
	DiskCacheBudget = 2 << 30 // 2 GiB

	// diskCacheEvictFraction is the slice of the budget eviction frees, as a
	// divisor: 8 means a save that does not fit evicts down to seven eighths
	// of the budget rather than to exactly the bytes it needs.
	//
	// Freeing only what one save needs would be correct and quadratic. Making
	// room re-derives usage from the directory (see scanLocked), which costs a
	// readdir plus a stat per entry, so a cache sitting at its budget would
	// pay that on every subsequent save for as long as it stayed full. Freeing
	// a fixed fraction amortises it, and does so independently of what is
	// being cached: the saves before the next scan and the entries a scan
	// walks both scale with the size of the objects, so their ratio — this
	// divisor — is the number of stats a save costs whether the cache holds
	// 256 packfiles or 32,000 v3 leaves.
	diskCacheEvictFraction = 8

	// tempSuffix marks an entry that has been written but not yet renamed into
	// place.
	tempSuffix = ".tmp"

	// tempStaleAfter is how long a temp file may sit unrenamed before it is
	// treated as the remains of a killed process rather than a save in flight.
	//
	// A threshold is needed because from outside the two are the same file:
	// deleting every temp file found in the directory would race a concurrent
	// process's save, and deleting none is what let an orphan survive every
	// eviction while occupying disk that nothing accounted for.
	//
	// An hour is three orders of magnitude past what a save takes — one
	// object's body, written and synced — so nothing legitimate reaches it,
	// and being wrong is cheap in both directions: a temp file removed from
	// under a live writer turns that writer's save into a cache miss and
	// nothing worse, while one left behind is collected by a later scan.
	tempStaleAfter = time.Hour

	// diskPromoteAfter is how many ranged reads of one object are served
	// remotely before the whole object is fetched and cached.
	//
	// It exists because the first ranged read of an object cannot know whether
	// a second will follow, and fetching an 8 MB blob to satisfy one 4 KB read
	// that never repeats is a 2,000x waste of bytes. Two is the smallest value
	// that requires evidence: one repeat is already enough to establish that
	// the caller is working through an aggregate rather than picking one
	// object out of it, and waiting longer only pays the remote cost again.
	diskPromoteAfter = 2

	// cacheBlockBytes is the granularity at which a cached body is hashed.
	//
	// The whole body cannot be the unit. Verification has to happen on every
	// read — an entry verified once and trusted afterwards serves corruption
	// that appears later in the same process — but hashing a whole 8 MB blob
	// to return a 4 KB member costs more than the remote request the cache is
	// replacing: measured at 4.55 s against 2.06 s for a restore whose backend
	// requests had gone to zero. Hashing per block makes the check proportional
	// to the bytes actually wanted.
	//
	// 64 KiB because it bounds the read amplification of a small member at
	// well under a millisecond of hashing while keeping the header small: a
	// full 8 MB blob needs 128 hashes, 4 KiB of header.
	cacheBlockBytes = 64 << 10

	// cacheFormatV1 is the first byte of a cache file, so a directory written
	// by a different layout is rejected as a miss instead of misparsed.
	cacheFormatV1 = 1

	// cacheHeaderFixed is the version byte plus the two uint32 fields ahead of
	// the block hashes.
	cacheHeaderFixed = 1 + 4 + 4

	// maxRangedCounters bounds the promotion counters.
	//
	// A counter is dropped as soon as its object is cached, so the map holds
	// only objects seen once and not yet promoted — but a read pattern that
	// touches each object exactly once never promotes any of them, and the map
	// would then grow with the repository. That is the residency failure this
	// whole layer exists to avoid, so it is bounded here rather than trusted
	// not to happen.
	//
	// Discarding counters costs at most one extra remote ranged read per
	// object whose count was forgotten, which is why the overflow simply
	// clears them rather than maintaining an eviction order for the cheapest
	// thing in the process.
	maxRangedCounters = 1 << 16
)

// diskCacheExcluded are the keys that must never be cached, stated as the
// exclusion rather than as a list of what may be.
//
// The rule is immutability, and it is the only rule: every other namespace is
// content-addressed, so a cached body can never be stale and the entry needs
// no invalidation. Naming the exceptions rather than the inclusions keeps the
// layer format-neutral, which is load-bearing rather than tidy — an earlier
// version cached `packs/` and `blob/` alone, which cached the whole of a v2
// repository (packs bundle every namespace) and only the file bodies of a v3
// one, and so measured the cache against itself rather than the two formats
// against each other.
//
//   - index/  is mutable by definition: index/latest is a pointer, and the
//     pack catalog is rewritten in place. A stale entry here is the one thing
//     the immutability argument does not cover.
//   - keys/   holds the wrapped master key. It is read once per open and is
//     tiny, so caching buys nothing, and key material has no business being
//     copied to a second location for no gain.
//   - config  is the repository marker, rewritten when the format is raised.
var diskCacheExcluded = []string{"index/", store.KeySlotPrefix, "config"}

func diskCacheable(key string) bool {
	for _, p := range diskCacheExcluded {
		if key == p || strings.HasPrefix(key, p) {
			return false
		}
	}
	return true
}

// caching reports whether this key should be served from, and written to, the
// cache right now.
func (c *DiskCacheStore) caching(key string) bool {
	return c.bypass.Load() == 0 && diskCacheable(key)
}

// DiskCacheStore serves whole-object reads and byte ranges from a local
// directory, fetching from the store beneath it on a miss.
//
// Safe for concurrent use within a process. Across processes it is safe by
// construction: every write lands through an atomic rename, so a name never
// appears until the whole file has been handed to the filesystem, and every
// file carries per-block hashes of the body it holds. The hashes rather than
// the rename are what a reader trusts — see save() on why nothing is synced. Processes sharing
// one directory also share its budget, because what the budget bounds is the
// directory — see scanLocked and reserveLocked.
type DiskCacheStore struct {
	store.ObjectStore

	dir    string
	budget int64

	// bypass turns the layer into a passthrough, for an operation whose
	// correctness depends on reading what the store actually holds.
	//
	// A toggle rather than a second chain because the chain's composition
	// order is itself a correctness invariant (see the package doc): building
	// a second one to vary a caching decision is a far larger risk than a flag.
	//
	// A count rather than a boolean, because a boolean restored to its previous
	// value is wrong under overlap: two checks on one client would have the
	// first to finish restore "off" while the second was still running, and the
	// second would then verify a local copy of the repository — the exact
	// failure the bypass exists to prevent. It is still client-wide, so an
	// operation that sets it slows any concurrent operation that wanted the
	// cache; it can never disable one that wanted the store.
	bypass atomic.Int64

	// deletes counts removals, so a read that began before one can decline to
	// write its result afterwards.
	//
	// A cache miss fetches from the store and then saves, and a Delete landing
	// between those two steps would otherwise be undone: the entry is dropped,
	// the object is removed from the backend, and the in-flight read puts it
	// back. A later read would then be served an object the repository no
	// longer has.
	//
	// One counter for the whole cache rather than a generation per key. Per
	// key is what this wants in the abstract, but such a map grows with the
	// number of keys ever deleted — the residency failure this layer's budget
	// exists to avoid, reintroduced beside it. Being coarse costs nothing that
	// matters: deletions come from prune and arrive in batches, so the reads it
	// invalidates are few and simply repopulate.
	deletes atomic.Uint64

	mu sync.Mutex
	// used is what the cache directory is believed to hold. It is an estimate
	// between scans and the directory's own answer immediately after one —
	// deliberately, because the budget is a bound on the directory rather than
	// on what one process remembers writing to it. Nothing may treat it as
	// authoritative; the only place it decides anything is reserveLocked,
	// which re-derives it before it says no.
	used int64
	// sinceScan is what this process has added since it last looked at the
	// directory, and so is exactly how stale `used` may be. Bounding it is
	// what bounds a *shared* directory: a process that consults nothing but
	// its own arithmetic until its own total reaches the budget can be a whole
	// budget behind what its neighbours have written, and the two of them then
	// hold twice the budget between them before either notices.
	sinceScan int64
	// pending is bytes charged by a save that has not yet reached the
	// directory. A scan reads what is there, and a save writes outside the
	// mutex, so without this a scan taken mid-save would adopt a directory
	// that does not yet contain the file whose bytes were already granted —
	// and the save would then land on top of a full cache. Counting them on
	// both sides while a save is in flight rounds the estimate up, which is
	// the direction that cannot break the bound.
	pending int64
	ranged  map[string]int // object key -> ranged reads served remotely
}

// cacheEntry is one evictable file, as a scan of the directory found it.
type cacheEntry struct {
	name string
	mod  int64
	size int64
}

// BypassReads makes every read go to the store beneath, and stops reads
// populating the cache, until the returned function is called. It nests: the
// cache comes back when the last caller releases, not the first.
//
// `check` is what this exists for. Its whole purpose is to notice that the
// repository's own bytes have gone bad, and an entry served from local disk is
// a verified copy of what *was* fetched, not evidence about what the store
// holds now. Reading a repository through a cache while verifying it would
// report the cache healthy.
//
// Safe on a nil receiver, which is what a client with no cache configured
// holds, so a caller needs no branch.
func (c *DiskCacheStore) BypassReads() func() {
	if c == nil {
		return func() {}
	}
	c.bypass.Add(1)
	var once sync.Once
	return func() { once.Do(func() { c.bypass.Add(-1) }) }
}

// NewDiskCacheStore opens (creating if needed) a cache under dir.
//
// An unusable directory is not an error the caller should have to handle: the
// cache is an optimisation, and no operation should fail because a cache
// directory is unwritable. The error is returned so a caller may report it,
// and the returned store is nil so the caller layers nothing.
func NewDiskCacheStore(inner store.ObjectStore, dir string, budget int64) (*DiskCacheStore, error) {
	if dir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("disk cache dir %s: %w", dir, err)
	}
	if budget <= 0 {
		// A caller with no opinion gets the default rather than a cache that
		// silently stores nothing, and no caller gets an unbounded one.
		budget = DiskCacheBudget
	}
	c := &DiskCacheStore{
		ObjectStore: inner,
		dir:         dir,
		budget:      budget,
		ranged:      map[string]int{},
	}
	// Adopt whatever is already there — this process's previous run, another
	// process, or a killed one's leftovers — rather than starting from the
	// assumption that the directory is empty.
	c.mu.Lock()
	_, err := c.scanLocked()
	c.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("disk cache dir %s: %w", dir, err)
	}
	return c, nil
}

// scanLocked re-derives what the cache directory holds, sweeping temp files
// old enough to be certain nobody is still writing them, and returns the
// evictable entries oldest-first.
//
// This is where the bound comes from, and making it a property of the
// directory rather than of this process's beliefs is the whole point. A
// counter seeded once at construction is wrong in three ways, all of them
// observed: a second cloudstic process writing into the same directory is
// invisible to it, so each enforces the budget alone and the directory reaches
// a multiple of it; a save killed between its write and its rename leaves a
// temp file that nothing ever counts or removes — one 8 MB orphan under a
// 1 MiB budget left 9.0x the budget on disk and survived every eviction; and
// any file the process failed to account for stays uncounted for its lifetime.
// Re-deriving costs one readdir and makes all three self-correcting, and it
// happens on the rare path only: a save that would exceed the budget, or one
// whose process has written a headroom's worth since it last looked.
//
// Temp files count towards usage but are not evictable: they occupy disk
// whether or not this process is allowed to delete them.
func (c *DiskCacheStore) scanLocked() ([]cacheEntry, error) {
	dirents, err := os.ReadDir(c.dir)
	if err != nil {
		// Leave the estimate as it stands: a directory that cannot be read is
		// also one nothing can be evicted from, so there is nothing better to
		// say about it.
		return nil, err
	}
	ents := make([]cacheEntry, 0, len(dirents))
	now := time.Now()
	var used int64
	for _, d := range dirents {
		if d.IsDir() {
			continue
		}
		info, err := d.Info()
		if err != nil {
			// Vanished between the readdir and the stat. Whoever removed it
			// is the one accounting for it.
			continue
		}
		if strings.HasSuffix(d.Name(), tempSuffix) {
			if now.Sub(info.ModTime()) < tempStaleAfter {
				used += info.Size() // someone's save, in flight
				continue
			}
			if err := os.Remove(filepath.Join(c.dir, d.Name())); err != nil {
				// Still there, so still spending disk. Counting it is what
				// stops it being ignored the way an orphan used to be.
				used += info.Size()
			}
			continue
		}
		used += info.Size()
		ents = append(ents, cacheEntry{name: d.Name(), mod: info.ModTime().UnixNano(), size: info.Size()})
	}
	c.used = used + c.pending
	c.sinceScan = 0
	sort.Slice(ents, func(i, j int) bool { return ents[i].mod < ents[j].mod })
	return ents, nil
}

// headroom is both the slice eviction frees and the number of unscanned bytes
// this process may write before it looks at the directory again. They are the
// same quantity on purpose: one scan per headroom of writes is what makes the
// scan's cost amortise (see diskCacheEvictFraction), and it is also what caps
// how far behind the directory a process's estimate can drift.
func (c *DiskCacheStore) headroom() int64 { return c.budget / diskCacheEvictFraction }

func (c *DiskCacheStore) Unwrap() store.ObjectStore { return c.ObjectStore }

// entryName is the cache file holding key's body.
//
// It is the hash of the *key*, not of the body, because only one of the cached
// namespaces is content-addressed by its bytes: a `packs/<hash>` ref is the
// SHA-256 of the pack, while a `blob/<hash>` ref is the hash of its members'
// digests in order and says nothing about the bytes on disk. Naming entries
// after the key keeps one scheme for every namespace, and the body is verified
// against the hashes stored inside the file instead.
func entryName(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func blockCount(bodyLen int) int {
	return (bodyLen + cacheBlockBytes - 1) / cacheBlockBytes
}

func headerLen(blocks int) int64 {
	return int64(cacheHeaderFixed + blocks*sha256.Size)
}

// encodeEntry builds the file contents for body: a small header of per-block
// hashes, then the body itself.
func encodeEntry(body []byte) []byte {
	blocks := blockCount(len(body))
	out := make([]byte, headerLen(blocks)+int64(len(body)))
	out[0] = cacheFormatV1
	binary.BigEndian.PutUint32(out[1:5], uint32(cacheBlockBytes))
	binary.BigEndian.PutUint32(out[5:9], uint32(blocks))
	for i := range blocks {
		lo := i * cacheBlockBytes
		hi := min(lo+cacheBlockBytes, len(body))
		sum := sha256.Sum256(body[lo:hi])
		copy(out[cacheHeaderFixed+i*sha256.Size:], sum[:])
	}
	copy(out[headerLen(blocks):], body)
	return out
}

// entryHeader is a cache file's parsed header.
type entryHeader struct {
	blockSize int
	blocks    int
	hashes    []byte // blocks * sha256.Size
	bodyAt    int64
	bodyLen   int64
}

// readHeader parses a cache file's header.
//
// Every length here comes from a file the process did not write in this run,
// so each is checked against the file's actual size before it is used to size
// anything. A block count read from a corrupted header would otherwise size an
// allocation.
func readHeader(f *os.File, fileSize int64) (entryHeader, bool) {
	var fixed [cacheHeaderFixed]byte
	if fileSize < cacheHeaderFixed {
		return entryHeader{}, false
	}
	if _, err := f.ReadAt(fixed[:], 0); err != nil {
		return entryHeader{}, false
	}
	if fixed[0] != cacheFormatV1 {
		return entryHeader{}, false
	}
	blockSize := int(binary.BigEndian.Uint32(fixed[1:5]))
	blocks := int(binary.BigEndian.Uint32(fixed[5:9]))
	if blockSize <= 0 || blocks < 0 {
		return entryHeader{}, false
	}
	hdrLen := headerLen(blocks)
	if hdrLen > fileSize {
		return entryHeader{}, false
	}
	bodyLen := fileSize - hdrLen
	// The header must describe exactly this body, or it is not this file's.
	if int64(blocks) != int64((bodyLen+int64(blockSize)-1)/int64(blockSize)) {
		return entryHeader{}, false
	}
	hashes := make([]byte, blocks*sha256.Size)
	if blocks > 0 {
		if _, err := f.ReadAt(hashes, cacheHeaderFixed); err != nil {
			return entryHeader{}, false
		}
	}
	return entryHeader{blockSize: blockSize, blocks: blocks, hashes: hashes, bodyAt: hdrLen, bodyLen: bodyLen}, true
}

// readVerified returns body bytes [offset, offset+length) from the entry,
// hashing every block those bytes fall in.
//
// The verification is not ceremony. A truncated or corrupted file would
// otherwise be sliced at a member's offset and handed up, and the AEAD above
// would reject it — but as a decryption failure on a healthy repository, which
// is far worse to diagnose than a cache miss. Doing it per block is what keeps
// the check proportional to the read rather than to the object.
func (c *DiskCacheStore) readVerified(key string, offset, length int64, whole bool) ([]byte, bool, error) {
	name := entryName(key)
	f, err := os.Open(filepath.Join(c.dir, name))
	if err != nil {
		return nil, false, nil
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, false, nil
	}
	h, ok := readHeader(f, info.Size())
	if !ok {
		c.dropEntry(name)
		return nil, false, nil
	}
	if whole {
		offset, length = 0, h.bodyLen
	}
	// Compared against the bytes remaining rather than as offset+length, which
	// overflows: MaxInt64 plus one wraps negative and passes a `> bodyLen`
	// test, after which the block arithmetic below sizes an allocation from a
	// wrapped value.
	if offset < 0 || length < 0 || offset > h.bodyLen || length > h.bodyLen-offset {
		return nil, true, fmt.Errorf("range %d+%d is outside %s (%d bytes)", offset, length, key, h.bodyLen)
	}
	if length == 0 {
		return []byte{}, true, nil
	}

	first := int(offset / int64(h.blockSize))
	last := int((offset + length - 1) / int64(h.blockSize))
	spanAt := int64(first) * int64(h.blockSize)
	spanEnd := min(int64(last+1)*int64(h.blockSize), h.bodyLen)
	span := make([]byte, spanEnd-spanAt)
	if _, err := f.ReadAt(span, h.bodyAt+spanAt); err != nil {
		c.dropEntry(name)
		return nil, false, nil
	}
	for i := first; i <= last; i++ {
		lo := int64(i)*int64(h.blockSize) - spanAt
		hi := min(lo+int64(h.blockSize), int64(len(span)))
		sum := sha256.Sum256(span[lo:hi])
		if !bytes.Equal(sum[:], h.hashes[i*sha256.Size:(i+1)*sha256.Size]) {
			// Not this body. Drop it rather than leaving a file that will fail
			// every future read.
			c.dropEntry(name)
			return nil, false, nil
		}
	}
	out := make([]byte, length)
	copy(out, span[offset-spanAt:offset-spanAt+length])
	return out, true, nil
}

// Beyond that, it evicts oldest-first until the body fits.
//
// Failures are swallowed deliberately: a full or read-only disk must slow an
// operation down, not stop one. The only thing that would make this unsafe is
// a partially written file being read back, which the rename prevents.
// save stores body under key, unless a deletion has landed since the read that
// produced it began — see the deletes counter.
func (c *DiskCacheStore) save(key string, body []byte, since uint64) {
	if c.deletes.Load() != since {
		return
	}
	encoded := encodeEntry(body)
	total := int64(len(encoded))
	if total > c.budget {
		return
	}
	name := entryName(key)
	path := filepath.Join(c.dir, name)
	// Already stored, by an earlier read or by another process sharing the
	// directory. Entries are immutable, so there is nothing to refresh, and
	// asking the filesystem rather than a map of what this process wrote is
	// what makes the second case a hit instead of a duplicate write.
	if _, err := os.Stat(path); err == nil {
		return
	}

	c.mu.Lock()
	room := c.reserveLocked(total)
	c.mu.Unlock()
	if !room {
		return
	}
	// The bytes are charged before they are written, so two saves racing
	// cannot both find the same room. Every path out from here that does not
	// store them has to give them back.
	stored := false
	defer func() {
		c.mu.Lock()
		c.pending -= total
		if !stored {
			c.used -= total
			c.sinceScan -= total
		}
		c.mu.Unlock()
	}()

	tmp, err := os.CreateTemp(c.dir, name+".*"+tempSuffix)
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	fail := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	if _, err := tmp.Write(encoded); err != nil {
		fail()
		return
	}
	// Deliberately not synced before the rename.
	//
	// The first version did sync, on the reasoning that a file which exists
	// should be a file that is complete, "which is what lets a concurrent
	// reader trust any name it finds". That reasoning is wrong about this
	// cache: a reader trusts the per-block hashes, never the name. A file torn
	// by a crash between write and rename fails verification on its next read
	// and is dropped as a miss, which is exactly what should happen to it —
	// the object is immutable and still in the store, so the cost of losing an
	// entry is one refetch.
	//
	// What the sync cost is not small. It is one fsync per entry, and an
	// operation that populates the cache pays it per object: a cold `find`
	// over a 25-snapshot v3 repository writes 728 entries and spent 3.2 of its
	// 4.9 seconds here, for a guarantee the block hashes already provide.
	// Dropping it takes that run to 1.76 s.
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return
	}
	stored = true
}

// reserveLocked charges want bytes to the cache, evicting oldest-first to make
// room, and reports whether they fit.
//
// It answers from the estimate while that estimate is both recent and
// comfortable, and from the directory itself otherwise, which is what keeps
// the budget a bound on the bytes actually present rather than on the ones
// this process remembers putting there.
//
// With one process the directory never exceeds the budget. With several
// sharing it, each is at most a headroom of its own writes behind what the
// others have done, so the directory settles at the budget and peaks at the
// budget plus one headroom per other process — against a multiple of the
// budget per process when the accounting was purely in memory.
func (c *DiskCacheStore) reserveLocked(want int64) bool {
	if c.used+want <= c.budget && c.sinceScan+want <= c.headroom() {
		c.chargeLocked(want)
		return true
	}
	ents, err := c.scanLocked()
	if err != nil {
		return false
	}
	low := c.budget - c.headroom()
	for _, e := range ents {
		if c.used+want <= low {
			break
		}
		if err := os.Remove(filepath.Join(c.dir, e.name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			continue
		}
		c.used -= e.size
	}
	if c.used+want > c.budget {
		// Nothing evictable is left and it still does not fit, so what remains
		// is other processes' saves in flight. Declining is the only answer
		// that keeps the bound: writing anyway is how a shared directory grew
		// to a multiple of the budget in the first place. The temp files
		// either land as entries or age out, and the next save finds room.
		return false
	}
	c.chargeLocked(want)
	return true
}

// chargeLocked books want bytes against the budget: spent, unscanned, and not
// yet on disk, which are three different questions about the same bytes.
func (c *DiskCacheStore) chargeLocked(want int64) {
	c.used += want
	c.sinceScan += want
	c.pending += want
}

func (c *DiskCacheStore) dropEntry(name string) {
	path := filepath.Join(c.dir, name)
	// Sized before it is removed, since the estimate has to be reduced by what
	// was actually there. A wrong answer here is survivable — the next scan
	// re-derives it — but a systematically low estimate delays that scan.
	var size int64
	if info, err := os.Stat(path); err == nil {
		size = info.Size()
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return
	}
	c.mu.Lock()
	c.used = max(c.used-size, 0)
	c.mu.Unlock()
}

// Get serves a cacheable object from disk when it is there, and caches what it
// has to fetch.
func (c *DiskCacheStore) Get(ctx context.Context, key string) ([]byte, error) {
	if !c.caching(key) {
		return c.ObjectStore.Get(ctx, key)
	}
	if body, hit, err := c.readVerified(key, 0, 0, true); hit {
		return body, err
	}
	since := c.deletes.Load()
	body, err := c.ObjectStore.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	c.save(key, body, since)
	return body, nil
}

// GetRange slices a cached body, or decides whether this object has earned a
// whole transfer.
//
// The decision is the whole design. Fetching an aggregate to satisfy one
// ranged read that never repeats multiplies bytes transferred by the ratio of
// object to member; serving every ranged read remotely is the amplification
// this layer exists to remove. Counting reads per object and promoting on the
// second is the smallest rule that distinguishes the two.
func (c *DiskCacheStore) GetRange(ctx context.Context, key string, offset, length int64) ([]byte, error) {
	if !c.caching(key) {
		return store.GetRange(ctx, c.ObjectStore, key, offset, length)
	}
	if out, hit, err := c.readVerified(key, offset, length, false); hit {
		return out, err
	}

	c.mu.Lock()
	if len(c.ranged) >= maxRangedCounters {
		clear(c.ranged)
	}
	c.ranged[key]++
	promote := c.ranged[key] >= diskPromoteAfter
	c.mu.Unlock()

	if !promote {
		return store.GetRange(ctx, c.ObjectStore, key, offset, length)
	}

	since := c.deletes.Load()
	body, err := c.ObjectStore.Get(ctx, key)
	if err != nil {
		// A whole fetch that fails must not fail the read the caller asked
		// for: the range may well still be servable.
		return store.GetRange(ctx, c.ObjectStore, key, offset, length)
	}
	c.save(key, body, since)
	c.mu.Lock()
	delete(c.ranged, key)
	c.mu.Unlock()
	return sliceBody(key, body, offset, length)
}

func sliceBody(key string, body []byte, offset, length int64) ([]byte, error) {
	// Bounds stated as remaining bytes, for the overflow reason in
	// readVerified: offset+length wraps, and this one slices on the result.
	size := int64(len(body))
	if offset < 0 || length < 0 || offset > size || length > size-offset {
		return nil, fmt.Errorf("range %d+%d is outside %s (%d bytes)", offset, length, key, len(body))
	}
	out := make([]byte, length)
	copy(out, body[offset:offset+length])
	return out, nil
}

// Delete evicts before forwarding, so a deleted object cannot be served from
// disk afterwards. It cannot be served *wrongly* — the bytes are immutable —
// but an entry for an object the repository no longer has spends the budget on
// data that will never be read again.
func (c *DiskCacheStore) Delete(ctx context.Context, key string) error {
	if diskCacheable(key) {
		c.forget(key)
	}
	return c.ObjectStore.Delete(ctx, key)
}

// DeleteAll forwards the batch, evicting each key first. Forwarding is safe
// here because Delete is a passthrough at this layer — unlike PackStore, which
// rewrites a catalog instead and so deliberately declines the capability.
func (c *DiskCacheStore) DeleteAll(ctx context.Context, keys []string) error {
	for _, k := range keys {
		if diskCacheable(k) {
			c.forget(k)
		}
	}
	return store.DeleteAll(ctx, c.ObjectStore, keys)
}

// DeleteAllSized is DeleteAll with the listing's sizes kept for the layers
// below, which is safe here for the same reason forwarding DeleteAll is.
func (c *DiskCacheStore) DeleteAllSized(ctx context.Context, objects []store.SizedKey) error {
	for _, o := range objects {
		if diskCacheable(o.Key) {
			c.forget(o.Key)
		}
	}
	return store.DeleteAllSized(ctx, c.ObjectStore, objects)
}

// ListSized implements store.SizedLister by forwarding. The cache holds copies
// of objects, never the set of them, so a listing is the backend's to answer
// — with or without sizes.
func (c *DiskCacheStore) ListSized(ctx context.Context, prefix string, fn func(key string, size int64) error) error {
	return store.ListSized(ctx, c.ObjectStore, prefix, fn)
}

func (c *DiskCacheStore) forget(key string) {
	// Bumped before the entry is dropped, so a save racing this cannot observe
	// the old value and then write after the drop.
	c.deletes.Add(1)
	c.dropEntry(entryName(key))
	c.mu.Lock()
	delete(c.ranged, key)
	c.mu.Unlock()
}
