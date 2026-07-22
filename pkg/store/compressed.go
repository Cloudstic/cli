package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
	"sync/atomic"

	"github.com/klauspost/compress/zstd"
)

var (
	zstdEncoder *zstd.Encoder
	zstdDecoder *zstd.Decoder
	zstdOnce    sync.Once
)

func initZstd() {
	zstdOnce.Do(func() {
		// Use default compression which provides excellent speed/ratio.
		// Set concurrency to 1 since our chunker already uploads chunks concurrently!
		var err error
		zstdEncoder, err = zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1), zstd.WithEncoderLevel(zstd.SpeedDefault))
		if err != nil {
			panic(err) // Should not fail with default options
		}
		zstdDecoder, err = zstd.NewReader(nil)
		if err != nil {
			panic(err)
		}
	})
}

// Framed object encoding.
//
// The encoding of a stored object is recorded in a header at write time rather
// than reconstructed on read by inspecting the bytes. Sniffing cannot tell "this
// store compressed the object" from "the user's own file begins with those
// bytes", and a backup tool that guesses wrong returns something other than what
// was backed up.
//
//	magic(4) || algorithm(1) || uncompressed length(8, big-endian) || payload
//
// The length field makes the frame self-checking: a payload that does not decode
// to exactly that many bytes is rejected rather than returned.
//
// Frames are detected on read regardless of whether this store writes them, so
// that a frame is never handed to the sniffing path. The cost is that an
// unframed legacy object whose first four bytes coincide with the magic is
// refused. That is deliberate, and the two outcomes are not equally likely: to
// be silently mis-decoded such an object would have to match the magic, carry a
// valid algorithm byte, and record a length its payload decodes to exactly,
// which is beyond negligible; to be refused it need only match the magic. An
// error is the acceptable half of that trade, and it is the half that keeps a
// mis-decode from reaching a restored file.
const (
	frameAlgoRaw  byte = 0x00 // payload is the object verbatim
	frameAlgoZstd byte = 0x01 // payload is zstd-compressed

	frameHeaderSize = 4 + 1 + 8
)

// frameMagic identifies a framed object. "CS" and a NUL, which no text payload
// carries in its first bytes.
var frameMagic = [4]byte{'C', 'S', 0x00, 0x01}

// ErrInvalidFrame reports a framed object whose header or payload is not
// self-consistent. It is never downgraded to "return the bytes as they are":
// that is the sniffing behaviour this frame exists to remove.
var ErrInvalidFrame = errors.New("store: invalid compression frame")

// CompressedStore wraps an ObjectStore and transparently zstd-compresses on
// write and decompresses on read.
//
// Writes are framed only when framing is enabled — see WithFramedWrites, and
// core.FramedCompressionFormat for why that is tied to the repository format.
// Reads accept both framed and unframed objects regardless: unframed objects
// exist in every repository indefinitely, because upgrades are opportunistic and
// permanently partial (docs/compatibility.md).
type CompressedStore struct {
	inner ObjectStore
	frame atomic.Bool
}

// CompressedOption configures a CompressedStore.
type CompressedOption func(*CompressedStore)

// WithFramedWrites enables the framed object encoding on write.
//
// It is off by default so that a store built without an established repository
// format — tests, and any caller that has not read the config — cannot write
// objects an older build would misread.
func WithFramedWrites(enabled bool) CompressedOption {
	return func(s *CompressedStore) { s.frame.Store(enabled) }
}

// FramedWrites reports whether this store frames what it writes.
func (s *CompressedStore) FramedWrites() bool { return s.frame.Load() }

// SetFramedWrites turns framing on once the repository is known to record a
// format whose readers understand the frame.
//
// It exists because that becomes true *during* a client's life: a mutation
// raises the format of an older repository before writing to it, and everything
// written from then on must be framed. Callers must enable framing before the
// first write of that mutation, not partway through — an unframed object written
// by this build is a permanent one, since content-addressed objects are never
// rewritten once stored.
func (s *CompressedStore) SetFramedWrites(enabled bool) { s.frame.Store(enabled) }

func (s *CompressedStore) Unwrap() ObjectStore { return s.inner }

func NewCompressedStore(inner ObjectStore, opts ...CompressedOption) *CompressedStore {
	initZstd()
	s := &CompressedStore{inner: inner}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *CompressedStore) Put(ctx context.Context, key string, data []byte) error {
	out := zstdEncoder.EncodeAll(data, make([]byte, 0, len(data)))
	algo := frameAlgoZstd
	if len(out) >= len(data) {
		out, algo = data, frameAlgoRaw
	}
	if s.frame.Load() {
		out = encodeFrame(algo, len(data), out)
	}
	return s.inner.Put(ctx, key, out)
}

// encodeFrame prefixes payload with the frame header describing how it was
// encoded and how many bytes it must decode to.
func encodeFrame(algo byte, rawLen int, payload []byte) []byte {
	out := make([]byte, frameHeaderSize, frameHeaderSize+len(payload))
	copy(out, frameMagic[:])
	out[4] = algo
	binary.BigEndian.PutUint64(out[5:], uint64(rawLen))
	return append(out, payload...)
}

func (s *CompressedStore) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := s.inner.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return decodeObject(data)
}

func (s *CompressedStore) Exists(ctx context.Context, key string) (bool, error) {
	return s.inner.Exists(ctx, key)
}

func (s *CompressedStore) Delete(ctx context.Context, key string) error {
	return s.inner.Delete(ctx, key)
}

func (s *CompressedStore) List(ctx context.Context, prefix string) ([]string, error) {
	return s.inner.List(ctx, prefix)
}

func (s *CompressedStore) Size(ctx context.Context, key string) (int64, error) {
	return s.inner.Size(ctx, key)
}

func (s *CompressedStore) TotalSize(ctx context.Context) (int64, error) {
	return s.inner.TotalSize(ctx)
}

func (s *CompressedStore) Flush(ctx context.Context) error {
	if f, ok := s.inner.(interface{ Flush(context.Context) error }); ok {
		return f.Flush(ctx)
	}
	return nil
}

// decodeObject reverses Put. A framed object is decoded from its header and
// validated against the recorded length; anything else falls back to sniffing,
// which is how objects written before framing — and objects in repositories
// still below core.FramedCompressionFormat — are read.
func decodeObject(data []byte) ([]byte, error) {
	if !isFramed(data) {
		return maybeDecompress(data)
	}

	algo := data[4]
	rawLen := binary.BigEndian.Uint64(data[5:frameHeaderSize])
	if rawLen > math.MaxInt {
		return nil, fmt.Errorf("%w: length %d exceeds addressable size", ErrInvalidFrame, rawLen)
	}
	payload := data[frameHeaderSize:]

	var out []byte
	switch algo {
	case frameAlgoRaw:
		out = payload
	case frameAlgoZstd:
		initZstd()
		dec, err := zstdDecoder.DecodeAll(payload, nil)
		if err != nil {
			return nil, fmt.Errorf("%w: zstd payload: %w", ErrInvalidFrame, err)
		}
		out = dec
	default:
		return nil, fmt.Errorf("%w: unknown algorithm 0x%02x", ErrInvalidFrame, algo)
	}

	if uint64(len(out)) != rawLen {
		return nil, fmt.Errorf("%w: decoded %d bytes, frame records %d", ErrInvalidFrame, len(out), rawLen)
	}
	return out, nil
}

// isFramed reports whether data carries a frame header. A legacy object whose
// first bytes coincide with the magic is the residual case the length check in
// decodeObject exists to catch.
func isFramed(data []byte) bool {
	return len(data) >= frameHeaderSize && bytes.HasPrefix(data, frameMagic[:])
}

// maybeDecompress is the legacy read path for unframed objects: it guesses the
// encoding from magic bytes. It cannot be removed — a repository is a permanent
// mixture of eras (docs/compatibility.md) — but it must not be reached for
// anything this build wrote framed.
func maybeDecompress(data []byte) ([]byte, error) {
	if len(data) < 2 {
		return data, nil
	}

	// Check gzip magic header (0x1f 0x8b)
	if data[0] == 0x1f && data[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return data, nil
		}
		defer func() { _ = zr.Close() }()
		return io.ReadAll(zr)
	}

	// Check zstd magic header (0x28 0xb5 0x2f 0xfd) Little-Endian
	if len(data) >= 4 && data[0] == 0x28 && data[1] == 0xb5 && data[2] == 0x2f && data[3] == 0xfd {
		initZstd()
		dec, err := zstdDecoder.DecodeAll(data, nil)
		if err != nil {
			// If not valid zstd, return as is (could be chunk content starting with same bytes)
			return data, nil
		}
		return dec, nil
	}

	return data, nil
}
