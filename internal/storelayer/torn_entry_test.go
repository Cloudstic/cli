package storelayer

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The guard that lets save() skip fsync.
//
// A crash between write and rename can leave a file at its final name whose
// tail was never flushed — missing, or zeros. The cache does not sync, so this
// is a state a reader can actually meet, and what must happen when it does is
// a miss and a refetch, never a torn body handed upward. Both shapes are
// tested for both read paths, because a whole-object Get and a ranged read
// verify different sets of blocks.
func TestDiskCacheStore_TornEntryIsAMissNotCorruption(t *testing.T) {
	ctx := context.Background()
	base := newRangeCounter(newLocal(t))
	body := bytes.Repeat([]byte("payload "), 40960) // 320 KB, several blocks
	key := blobKey(7000)
	mustPut(t, ctx, base, key, body)

	for _, tc := range []struct {
		name string
		tear func(raw []byte) []byte
	}{
		{"truncated tail", func(r []byte) []byte { return r[:len(r)-50_000] }},
		{"zero-filled tail", func(r []byte) []byte {
			out := append([]byte(nil), r...)
			for i := len(out) - 50_000; i < len(out); i++ {
				out[i] = 0
			}
			return out
		}},
		{"zero-filled whole body", func(r []byte) []byte {
			out := append([]byte(nil), r...)
			for i := cacheHeaderFixed; i < len(out); i++ {
				out[i] = 0
			}
			return out
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, dir := newDiskCache(t, base, DiskCacheBudget)
			if _, err := c.Get(ctx, key); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, entryName(key))
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, tc.tear(raw), 0o600); err != nil {
				t.Fatal(err)
			}

			got, err := c.Get(ctx, key)
			if err != nil {
				t.Fatalf("Get over a torn entry: %v", err)
			}
			if !bytes.Equal(got, body) {
				t.Fatal("a torn entry was served instead of the store's bytes")
			}
			out, err := c.GetRange(ctx, key, 100, 256)
			if err != nil {
				t.Fatalf("GetRange over a torn entry: %v", err)
			}
			if !bytes.Equal(out, body[100:356]) {
				t.Fatal("a torn entry served a wrong range")
			}
		})
	}
}
