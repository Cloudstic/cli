package blob

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/cloudstic/cli/pkg/crypto"
)

// benchBodies builds n bodies of about size bytes each, compressible in the
// way real file content is rather than either extreme: random bytes would make
// compression a no-op and a repeated string would make it free.
func benchBodies(n, size int) ([][]byte, []string) {
	bodies := make([][]byte, n)
	hashes := make([]string, n)
	for i := range n {
		b := make([]byte, size)
		// A quarter random, the rest structured — roughly what source trees give.
		_, _ = rand.Read(b[:size/4])
		for j := size / 4; j < size; j++ {
			b[j] = byte('a' + (i+j)%16)
		}
		sum := sha256.Sum256(b)
		bodies[i], hashes[i] = b, hex.EncodeToString(sum[:])
	}
	return bodies, hashes
}

func benchSealer(b *testing.B) *crypto.MemberSealer {
	b.Helper()
	key := sha256.Sum256([]byte("bench"))
	s, err := crypto.NewMemberSealer(key[:])
	if err != nil {
		b.Fatal(err)
	}
	return s
}

// Sealing a blob is on the backup path, once per blob. The interesting number
// is per member: a 4 MB blob of small files holds hundreds.
func BenchmarkWriterSeal(b *testing.B) {
	for _, size := range []int{4 << 10, 64 << 10} {
		n := (4 << 20) / size // members in a 4 MB blob
		b.Run(fmt.Sprintf("%dKiB/%dmembers", size>>10, n), func(b *testing.B) {
			bodies, hashes := benchBodies(n, size)
			s := benchSealer(b)
			b.SetBytes(int64(n * size))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				w := NewWriter(s)
				for i := range bodies {
					if err := w.Add(hashes[i], bodies[i]); err != nil {
						b.Fatal(err)
					}
				}
				if _, _, _, err := w.Seal(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// The read the format exists for. This is the per-entry cost restore pays, and
// what has to stay small for a ranged read to be worth its request.
func BenchmarkReadMember(b *testing.B) {
	for _, size := range []int{4 << 10, 64 << 10} {
		b.Run(fmt.Sprintf("%dKiB", size>>10), func(b *testing.B) {
			n := (4 << 20) / size
			bodies, hashes := benchBodies(n, size)
			s := benchSealer(b)
			w := NewWriter(s)
			for i := range bodies {
				if err := w.Add(hashes[i], bodies[i]); err != nil {
					b.Fatal(err)
				}
			}
			ref, data, members, err := w.Seal()
			if err != nil {
				b.Fatal(err)
			}
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for i := range b.N {
				m := members[i%len(members)]
				if _, err := ReadMember(data[m.Offset:m.Offset+m.Length], m.ContentHash, ref, s, m.PlainSize); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// check and any consolidation pass read the index rather than the members, so
// its cost is what a whole-repository sweep multiplies by the blob count.
func BenchmarkParseIndex(b *testing.B) {
	bodies, hashes := benchBodies(1024, 4<<10)
	s := benchSealer(b)
	w := NewWriter(s)
	for i := range bodies {
		if err := w.Add(hashes[i], bodies[i]); err != nil {
			b.Fatal(err)
		}
	}
	ref, data, _, err := w.Seal()
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := ParseIndex(data, ref, s); err != nil {
			b.Fatal(err)
		}
	}
}

// The same bytes compressed as ONE object, which is what the store chain does
// for every object today. The gap is what per-member framing costs, and it is
// the price of a ranged read rather than an inefficiency to remove: zstd
// restarts per member, so it never sees redundancy across members.
//
// Measured on an M3 Max against BenchmarkWriterSeal over the same 4 MB:
// 4 KiB members 17.4ms against 3.2ms one-shot (5.4x), 64 KiB members 3.2ms
// against 1.3ms (2.5x). Member size dominates, which is worth knowing before
// the inline threshold is re-chosen — it is now the knob that decides how much
// of a repository is sealed in small pieces.
func BenchmarkWholeObjectCompress(b *testing.B) {
	initZstd()
	for _, size := range []int{4 << 10, 64 << 10} {
		n := (4 << 20) / size
		bodies, _ := benchBodies(n, size)
		var whole []byte
		for _, body := range bodies {
			whole = append(whole, body...)
		}
		b.Run(fmt.Sprintf("from%dKiBbodies", size>>10), func(b *testing.B) {
			b.SetBytes(int64(len(whole)))
			b.ReportAllocs()
			for range b.N {
				_ = zstdEncoder.EncodeAll(whole, nil)
			}
		})
	}
}
