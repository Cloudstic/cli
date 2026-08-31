package hamt

import (
	"os"
	"strconv"
)

// Test-only overrides for the two format-v3 tunables whose right values are a
// measurement rather than an argument: how large a leaf grows before it splits,
// and how many bytes of nodes the read cache holds.
//
// They exist because sweeping either otherwise costs a rebuild per data point,
// which rules out the harness's POLICIES axis — the mechanism that compares
// variants against *one* aged repository rather than against repositories that
// differ in how they were built (see AGENTS.md, RFC 0025 §7). Issues #524 and
// #525 are the sweeps these serve.
//
// CLOUDSTIC_TEST_* is the module's convention for a knob that is not part of
// the user-facing surface: nothing documents these, no flag reaches them, and
// production behaviour is exactly the constants below when they are unset.
//
// The leaf budget is a write-path knob, so a repository written under an
// override keeps whatever leaf sizes it was given — a reader accepts any size,
// which is what makes the override safe as well as useful. The cache budget is
// read-side and affects nothing stored.
const (
	envLeafSplitBytesV3 = "CLOUDSTIC_TEST_LEAF_BYTES"
	envNodeCacheBytesV3 = "CLOUDSTIC_TEST_NODE_CACHE_BYTES"
)

// v3LeafSplitBytes returns the byte budget a v3 leaf splits at.
//
// Call it once, when a node store is put in v3 mode — NodeStore.leafBytes is
// where the answer lives, and leafOverfull reads that. The split rule runs
// once per entry of a leaf on every insert, so consulting the environment
// there is an os.LookupEnv per entry per insert: 19% of a no-change backup's
// CPU on a 20,000-file tree, more than the whole source walk (issue #538).
func v3LeafSplitBytes() int {
	return envInt(envLeafSplitBytesV3, leafSplitBytesV3)
}

// v3NodeCacheBytes returns the v3 node cache's byte budget. It defaults to a
// multiple of the *effective* leaf budget rather than the constant, so
// overriding the leaf size alone keeps the cache holding the same number of
// leaves — which is the quantity the default is actually expressed in.
func v3NodeCacheBytes() int {
	return envInt(envNodeCacheBytesV3, nodeCacheLeaves*v3LeafSplitBytes())
}

// envInt reads a positive integer from the environment, falling back to def
// for an unset, empty, malformed or non-positive value. A bad value falls back
// rather than failing: these are diagnostic knobs, and a typo in one should not
// take down a backup.
func envInt(name string, def int) int {
	v, ok := os.LookupEnv(name)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
