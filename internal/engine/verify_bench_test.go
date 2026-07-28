package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/cloudstic/cli/internal/core"
)

// The integrity check getVerified performs is a SHA-256 over bytes the caller
// has already paid to fetch and is about to pay to decode. These benchmarks
// measure it against that baseline rather than in isolation, because "how much
// slower is a metadata read" is the question a user has, and an isolated hash
// benchmark would answer a different one.

func benchFileMetaCorpus(b *testing.B, n int) (*MockStore, []string) {
	b.Helper()
	s := NewMockStore()
	refs := make([]string, 0, n)
	ctx := context.Background()
	for i := range n {
		fm := core.FileMeta{
			Version:     1,
			FileID:      fmt.Sprintf("file-%d", i),
			Name:        fmt.Sprintf("some-reasonably-named-document-%d.txt", i),
			Type:        core.FileTypeFile,
			Parents:     []string{"parent-directory-id"},
			ContentHash: fmt.Sprintf("%064x", i),
			Size:        int64(i * 1024),
			Mtime:       1700000000,
			Owner:       "user@example.com",
		}
		ref, data, err := core.FileMetaRef(&fm)
		if err != nil {
			b.Fatalf("ref: %v", err)
		}
		if err := s.Put(ctx, ref, data); err != nil {
			b.Fatalf("put: %v", err)
		}
		refs = append(refs, ref)
	}
	return s, refs
}

// BenchmarkFileMetaRead_Verified is the path every reader now takes.
func BenchmarkFileMetaRead_Verified(b *testing.B) {
	s, refs := benchFileMetaCorpus(b, 1000)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		ref := refs[i%len(refs)]
		data, err := getVerified(ctx, s, ref)
		if err != nil {
			b.Fatal(err)
		}
		var fm core.FileMeta
		if err := json.Unmarshal(data, &fm); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFileMetaRead_Unverified is the path readers took before, kept as the
// comparison baseline.
func BenchmarkFileMetaRead_Unverified(b *testing.B) {
	s, refs := benchFileMetaCorpus(b, 1000)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		ref := refs[i%len(refs)]
		data, err := s.Get(ctx, ref)
		if err != nil {
			b.Fatal(err)
		}
		var fm core.FileMeta
		if err := json.Unmarshal(data, &fm); err != nil {
			b.Fatal(err)
		}
	}
}
