package engine

import (
	"io"

	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/internal/ui"
	"github.com/cloudstic/cli/pkg/store"
)

// Deps is the ambient dependency set every engine manager is built from: the
// repository it works against, where progress is reported, where debug output
// goes, and the key content addressing is computed under.
//
// It is one value rather than a positional parameter each because those four
// appeared in five different orders across the constructors, and Reporter and
// LogSink are both "somewhere output goes" — a transposition the compiler
// cannot catch. Passing them by name also stopped three managers from silently
// having no debug sink at all: restore, check and prune took no log writer,
// which read as an omission rather than a decision.
//
// Not every manager uses every field. A manager that does not report progress
// ignores Reporter, and one that does not content-address ignores HMACKey.
type Deps struct {
	// Store is the repository this manager reads and writes. Required.
	Store store.ObjectStore

	// Reporter receives phase and progress updates. The long-running
	// operations — backup, restore, check, prune, forget, copy — call into it
	// unconditionally, so pass ui.NewNoOpReporter() rather than nil.
	Reporter ui.Reporter

	// LogSink receives component-prefixed debug output. A nil sink discards,
	// so a caller with nowhere to send it can leave this zero.
	LogSink io.Writer

	// HMACKey is the dedup key that content refs are derived under. Nil for a
	// repository with no encryption.
	HMACKey []byte

	// FormatV3 marks the repository as recorded format v3 (RFC 0026): trees
	// seal binary fat leaves whose entries carry their metadata and small
	// content as payloads, and the engine neither writes nor reads standalone
	// filemeta/ or content/ objects. Set from the repository's recorded
	// version at client open, never per operation.
	FormatV3 bool
}

// treeOptions returns the hamt options this dependency set implies, with any
// extras appended — so a manager cannot construct a tree that disagrees with
// the repository's format.
func (d Deps) treeOptions(extra ...hamt.TreeOption) []hamt.TreeOption {
	var opts []hamt.TreeOption
	if d.FormatV3 {
		opts = append(opts, hamt.WithFormatV3())
	}
	return append(opts, extra...)
}
