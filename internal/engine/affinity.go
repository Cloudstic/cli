package engine

import "github.com/cloudstic/cli/internal/core"

// AffinityKey produces a locality-preserving HAMT routing key.
//
// parentID is the raw source-level parent identifier ("" for root-level
// entries); fileID is the raw source-level file identifier. Borrowing the
// leading routing bits from the parent makes files sharing a parent share the
// top levels of the trie, so backing up a single changed directory rewrites a
// narrow spine of metadata instead of a scattered one.
//
// This is backup policy, not a property of the HAMT: the tree only requires a
// hex routing key and never interprets its structure. See RFC 0002.
func AffinityKey(parentID, fileID string) string {
	parentHash := core.ComputeHash([]byte(parentID))
	fileHash := core.ComputeHash([]byte(fileID))
	return parentHash[:4] + fileHash[4:]
}
