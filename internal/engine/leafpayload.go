package engine

import "github.com/cloudstic/cli/internal/hamt"

// contentBody is where one entry's content lives, reduced to the shape both
// repository formats agree on: a total size, and then either the bytes
// themselves or the ordered chunk refs that reconstruct them.
//
// It is the seam between reading content and writing it. A format-v2
// repository spells this as a "content/<hash>" object and a v3 one as part of
// the leaf entry, but the triple is the same either way, which is what lets
// copy read one format and write the other without a second encoding step.
type contentBody struct {
	// size is the entry's content size in bytes. Zero for folders.
	size int64

	// body is the entry's whole content, for content small enough not to be
	// chunked. Nil when the content is chunked or empty.
	//
	// It is the bytes rather than a reference because this is the seam between
	// reading content and writing it: where the bytes end up is the
	// destination repository's business, and in v3 that is a blob whose
	// identity is not known until it seals.
	body []byte

	// chunks is the ordered list of "chunk/<hash>" refs reconstructing the
	// content, named under the repository the body is bound for. Nil when the
	// content is inline or empty.
	chunks []string
}

// newLeafPayload builds the format-v3 leaf body for one entry: the canonical
// metadata bytes whose content address is the entry's value, together with the
// content those bytes name (RFC 0026 §2).
//
// Every v3 entry goes through here, whichever operation produced it. A
// repository written by copy has to be indistinguishable from one written by
// backup — same entry values, same payloads, therefore the same node refs and
// the same root hash — and the only way to guarantee that is for the two not
// to have separate opinions about how a payload is assembled.
//
// Passing a zero contentBody yields a metadata-only payload, which is what a
// folder and an empty file carry.
func newLeafPayload(meta []byte, body contentBody) *hamt.Payload {
	p := &hamt.Payload{Meta: meta, Size: body.size}
	// A body and chunks are alternatives, never both: a repository that
	// recorded one of each for the same entry would restore it twice over.
	// A body's own placement is filled in by whoever seals the blob holding
	// it, so a payload leaves here with Body still nil.
	if len(body.chunks) > 0 {
		p.Chunks = body.chunks
	}
	return p
}
