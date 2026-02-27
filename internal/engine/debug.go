package engine

import "io"

// DebugWriter, when non-nil, receives debug output from engine internals
// (cache stats, index reconciliation, cache misses). Set from main when the
// -debug flag is active.
var DebugWriter io.Writer
