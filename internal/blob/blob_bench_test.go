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

// Sealing a blob is on the backup path, once per blob. It covers hashing each
// body to verify the caller's digest, compressing and sealing every member,
// sealing the index and assembling the object — so it is deliberately not
// comparable with the compression-only baselines further down. Subtract
// BenchmarkPerMemberCompress from it to see what everything other than
// compression costs: on an M3 Max, 3.4ms of a 17.5ms 4 KiB-member blob and
// 1.9ms of a 3.1ms 64 KiB-member one.
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

// BenchmarkPerMemberCompress isolates the one thing the layout changes about
// compression: zstd restarts per member and never sees redundancy across them.
// It compresses the same bytes member by member and does nothing else, so it
// is directly comparable with BenchmarkWholeObjectCompress below. This pair,
// and not BenchmarkWriterSeal against the baseline, is what measures framing.
//
// On an M3 Max over the same 4 MB: at 4 KiB members 14.1ms against 3.2ms
// one-shot, a 4.4x penalty; at 64 KiB members 1.20ms against 1.34ms, which is
// no penalty at all — per-member is marginally the faster of the two, since
// zstd on 64 KiB already saturates its window and the one-shot path pays for a
// single 4 MB buffer.
//
// So the cost of framing is not a property of the layout so much as of member
// size, and it is confined to small members. That sharpens what the inline
// threshold now decides: it is the knob choosing how much of a repository is
// compressed in pieces too small to amortise a zstd restart.
func BenchmarkPerMemberCompress(b *testing.B) {
	for _, size := range []int{4 << 10, 64 << 10} {
		n := (4 << 20) / size
		bodies, _ := benchBodies(n, size)
		b.Run(fmt.Sprintf("%dKiBmembers", size>>10), func(b *testing.B) {
			b.SetBytes(int64(n * size))
			b.ReportAllocs()
			for range b.N {
				for _, body := range bodies {
					// A fresh, exactly-sized destination per member, which is
					// what the writer used to do. Reusing one here would
					// measure the writer's buffer reuse rather than the zstd
					// restart this benchmark exists to price, and would stop
					// it being comparable with the numbers recorded above.
					_ = appendCompressed(make([]byte, 0, len(body)+1), body)
				}
			}
		})
	}
}

// The same bytes compressed as ONE object, which is what the store chain does
// for every object today. Against BenchmarkPerMemberCompress the gap is the
// price of a ranged read rather than an inefficiency to remove.
func BenchmarkWholeObjectCompress(b *testing.B) {
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
				_ = encoder().EncodeAll(whole, nil)
			}
		})
	}
}
