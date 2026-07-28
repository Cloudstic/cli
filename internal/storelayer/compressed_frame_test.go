package storelayer

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"testing"
)

// gzipBytes returns a valid gzip stream, i.e. bytes that begin with the gzip
// magic header without being anything this store produced.
func gzipBytes(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(payload); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// zstdBytes returns a valid zstd stream, for the same reason as gzipBytes.
func zstdBytes(t *testing.T, payload []byte) []byte {
	t.Helper()
	initZstd()
	return zstdEncoder.EncodeAll(payload, nil)
}

// TestCompressedStore_FramedRoundTripsAlreadyCompressedData is the regression
// test for the corruption this frame exists to prevent: a user's own .gz or
// .zst file that zstd cannot shrink is stored verbatim, and an unframed read
// then inflates it and hands back something other than the file.
func TestCompressedStore_FramedRoundTripsAlreadyCompressedData(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name string
		data []byte
	}{
		// Small enough to land in a single chunk: the branch that silently
		// returned the inflated content.
		{"single-chunk gzip", gzipBytes(t, bytes.Repeat([]byte("A"), 7500))},
		{"single-chunk zstd", zstdBytes(t, bytes.Repeat([]byte("A"), 7500))},
		// Large enough that the truncated read failed loudly instead.
		{"multi-chunk gzip", gzipBytes(t, bytes.Repeat([]byte("hello world "), 400_000))},
		{"multi-chunk zstd", zstdBytes(t, bytes.Repeat([]byte("hello world "), 400_000))},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewCompressedStore(newMemStore(), WithFramedWrites(true))
			if err := s.Put(ctx, "chunk/x", tc.data); err != nil {
				t.Fatalf("Put: %v", err)
			}
			got, err := s.Get(ctx, "chunk/x")
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if !bytes.Equal(got, tc.data) {
				t.Fatalf("round trip corrupted data: put %d bytes, got back %d", len(tc.data), len(got))
			}
		})
	}
}

func TestCompressedStore_FramedRoundTripsBothBranches(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"tiny incompressible", []byte("tiny")},
		{"highly compressible", bytes.Repeat([]byte("A"), 10*1024)},
		{"leading NUL bytes", append([]byte{0, 0, 0, 0}, []byte("payload")...)},
		// Begins with the frame magic without being a frame.
		{"payload wearing the magic", append(frameMagic[:], []byte("not a frame at all")...)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewCompressedStore(newMemStore(), WithFramedWrites(true))
			if err := s.Put(ctx, "chunk/x", tc.data); err != nil {
				t.Fatalf("Put: %v", err)
			}
			got, err := s.Get(ctx, "chunk/x")
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if !bytes.Equal(got, tc.data) {
				t.Fatalf("round trip mismatch: want %q, got %q", tc.data, got)
			}
		})
	}
}

// TestCompressedStore_FramedWritesUseBothAlgorithms pins the stored-raw branch:
// incompressible input must be framed as raw, compressible input as zstd.
func TestCompressedStore_FramedWritesUseBothAlgorithms(t *testing.T) {
	ctx := context.Background()
	mem := newMemStore()
	s := NewCompressedStore(mem, WithFramedWrites(true))

	raw := gzipBytes(t, []byte("incompressible enough that zstd gives up"))
	compressible := bytes.Repeat([]byte("A"), 10*1024)

	if err := s.Put(ctx, "chunk/raw", raw); err != nil {
		t.Fatalf("Put raw: %v", err)
	}
	if err := s.Put(ctx, "chunk/zstd", compressible); err != nil {
		t.Fatalf("Put compressible: %v", err)
	}

	if got := mem.data["chunk/raw"][4]; got != frameAlgoRaw {
		t.Errorf("incompressible object: algorithm = 0x%02x, want raw (0x%02x)", got, frameAlgoRaw)
	}
	if got := mem.data["chunk/zstd"][4]; got != frameAlgoZstd {
		t.Errorf("compressible object: algorithm = 0x%02x, want zstd (0x%02x)", got, frameAlgoZstd)
	}
	if len(mem.data["chunk/zstd"]) >= len(compressible) {
		t.Errorf("compressible object was not compressed: stored %d bytes for %d", len(mem.data["chunk/zstd"]), len(compressible))
	}
}

// TestCompressedStore_RejectsInconsistentFrame covers the frame's whole point:
// a frame that does not decode to the length it records is an error, never data.
func TestCompressedStore_RejectsInconsistentFrame(t *testing.T) {
	ctx := context.Background()

	frameWith := func(mutate func([]byte) []byte) []byte {
		f := encodeFrame(frameAlgoRaw, 5, []byte("hello"))
		return mutate(f)
	}

	cases := []struct {
		name  string
		frame []byte
	}{
		{"length larger than payload", frameWith(func(f []byte) []byte {
			binary.BigEndian.PutUint64(f[5:], 99)
			return f
		})},
		{"length smaller than payload", frameWith(func(f []byte) []byte {
			binary.BigEndian.PutUint64(f[5:], 2)
			return f
		})},
		{"truncated payload", frameWith(func(f []byte) []byte {
			return f[:len(f)-2]
		})},
		{"unknown algorithm", frameWith(func(f []byte) []byte {
			f[4] = 0x7f
			return f
		})},
		{"zstd algorithm over non-zstd payload", frameWith(func(f []byte) []byte {
			f[4] = frameAlgoZstd
			return f
		})},
		{"bit-flipped zstd payload", func() []byte {
			payload := zstdBytes(t, bytes.Repeat([]byte("A"), 1024))
			payload[len(payload)/2] ^= 0xff
			return encodeFrame(frameAlgoZstd, 1024, payload)
		}()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mem := newMemStore()
			mem.data["chunk/x"] = tc.frame
			s := NewCompressedStore(mem, WithFramedWrites(true))

			got, err := s.Get(ctx, "chunk/x")
			if err == nil {
				t.Fatalf("Get returned %d bytes, want an error", len(got))
			}
			if !errors.Is(err, ErrInvalidFrame) {
				t.Errorf("error = %v, want ErrInvalidFrame", err)
			}
		})
	}
}

// TestCompressedStore_ReadsUnframedObjects covers the fallback that
// docs/compatibility.md makes permanent: objects written before framing, and
// objects written into a repository still below the gating format, stay
// readable by a store that frames its own writes.
func TestCompressedStore_ReadsUnframedObjects(t *testing.T) {
	ctx := context.Background()
	mem := newMemStore()

	legacy := NewCompressedStore(mem)
	framing := NewCompressedStore(mem, WithFramedWrites(true))

	cases := []struct {
		name string
		data []byte
	}{
		{"compressible", bytes.Repeat([]byte("A"), 10*1024)},
		{"incompressible", []byte("tiny")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := legacy.Put(ctx, "chunk/"+tc.name, tc.data); err != nil {
				t.Fatalf("legacy Put: %v", err)
			}
			if isFramed(mem.data["chunk/"+tc.name]) {
				t.Fatal("legacy store wrote a frame")
			}
			got, err := framing.Get(ctx, "chunk/"+tc.name)
			if err != nil {
				t.Fatalf("framing Get of legacy object: %v", err)
			}
			if !bytes.Equal(got, tc.data) {
				t.Errorf("legacy object misread: want %q, got %q", tc.data, got)
			}
		})
	}
}

// TestCompressedStore_LegacyObjectWearingTheMagicIsRefused documents the
// residual collision. An unframed object whose first bytes happen to match the
// magic is refused rather than mis-decoded. Refusing is the acceptable half of
// that trade: a mis-decode would reach a restored file, an error cannot.
func TestCompressedStore_LegacyObjectWearingTheMagicIsRefused(t *testing.T) {
	ctx := context.Background()
	mem := newMemStore()

	// Written by a build predating the frame, stored raw because zstd could not
	// shrink it, and beginning with the bytes the frame now claims.
	mem.data["chunk/x"] = append(frameMagic[:], zstdBytes(t, []byte("payload"))...)

	if _, err := NewCompressedStore(mem).Get(ctx, "chunk/x"); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("error = %v, want ErrInvalidFrame", err)
	}
}

// TestCompressedStore_UnframedByDefault pins the write gate. A store built
// without an established repository format must not emit frames, because a
// build predating the frame returns one verbatim instead of refusing it.
func TestCompressedStore_UnframedByDefault(t *testing.T) {
	ctx := context.Background()
	mem := newMemStore()
	s := NewCompressedStore(mem)

	if err := s.Put(ctx, "chunk/x", bytes.Repeat([]byte("A"), 1024)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if isFramed(mem.data["chunk/x"]) {
		t.Error("default store wrote a framed object")
	}

	if err := NewCompressedStore(mem, WithFramedWrites(false)).Put(ctx, "chunk/y", []byte("tiny")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if isFramed(mem.data["chunk/y"]) {
		t.Error("WithFramedWrites(false) wrote a framed object")
	}
}
