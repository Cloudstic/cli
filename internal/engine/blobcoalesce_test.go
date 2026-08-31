package engine

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudstic/cli/internal/hamt"
	"github.com/cloudstic/cli/pkg/store"
)

func ref(blob string, offset, length int64) *hamt.BodyRef {
	return &hamt.BodyRef{Blob: blob, Offset: offset, Length: length, Total: offset + length}
}

// The gap rule is the whole planner: two reads merge when the bytes between
// them cost less to transfer than a second round trip, and stay apart when
// they do not.
func TestPlanBlobSpansMergesOnTheGapRule(t *testing.T) {
	const gap = 1024

	tests := []struct {
		name  string
		reads []blobRead
		want  []blobSpan
	}{
		{
			name: "adjacent members become one read",
			reads: []blobRead{
				{index: 0, ref: ref("blob/a", 0, 100)},
				{index: 1, ref: ref("blob/a", 100, 50)},
			},
			want: []blobSpan{{blob: "blob/a", offset: 0, length: 150, members: []int{0, 1}}},
		},
		{
			name: "a gap under the rule is transferred rather than skipped",
			reads: []blobRead{
				{index: 0, ref: ref("blob/a", 0, 100)},
				{index: 1, ref: ref("blob/a", 100+gap, 50)},
			},
			want: []blobSpan{{blob: "blob/a", offset: 0, length: 100 + gap + 50, members: []int{0, 1}}},
		},
		{
			name: "a gap over the rule costs its own request",
			reads: []blobRead{
				{index: 0, ref: ref("blob/a", 0, 100)},
				{index: 1, ref: ref("blob/a", 101+gap, 50)},
			},
			want: []blobSpan{
				{blob: "blob/a", offset: 0, length: 100, members: []int{0}},
				{blob: "blob/a", offset: 101 + gap, length: 50, members: []int{1}},
			},
		},
		{
			name: "reads are merged per blob, never across two",
			reads: []blobRead{
				{index: 0, ref: ref("blob/a", 0, 10)},
				{index: 1, ref: ref("blob/b", 10, 10)},
				{index: 2, ref: ref("blob/a", 10, 10)},
			},
			want: []blobSpan{
				{blob: "blob/a", offset: 0, length: 20, members: []int{0, 2}},
				{blob: "blob/b", offset: 10, length: 10, members: []int{1}},
			},
		},
		{
			name: "walk order does not have to be offset order",
			reads: []blobRead{
				{index: 0, ref: ref("blob/a", 200, 50)},
				{index: 1, ref: ref("blob/a", 0, 50)},
				{index: 2, ref: ref("blob/a", 50, 50)},
			},
			want: []blobSpan{{blob: "blob/a", offset: 0, length: 250, members: []int{1, 2, 0}}},
		},
		{
			// The blob writer stores one member per content hash, so two
			// entries with the same content name the same bytes. They must
			// come back as one read serving both, not as a duplicate.
			name: "two entries sharing one member share its read",
			reads: []blobRead{
				{index: 0, ref: ref("blob/a", 40, 60)},
				{index: 1, ref: ref("blob/a", 40, 60)},
			},
			want: []blobSpan{{blob: "blob/a", offset: 40, length: 60, members: []int{0, 1}}},
		},
		{
			name:  "nothing to read plans nothing",
			reads: nil,
			want:  nil,
		},
		{
			name: "an entry with no body reference is not a read",
			reads: []blobRead{
				{index: 0, ref: nil},
				{index: 1, ref: ref("blob/a", 0, 10)},
			},
			want: []blobSpan{{blob: "blob/a", offset: 0, length: 10, members: []int{1}}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := planBlobSpans(tc.reads, gap, restoreSpanBytes())
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("planBlobSpans\n got %v\nwant %v", got, tc.want)
			}
		})
	}
}

// Coalescing must not become an unbounded read. A blob whose wanted members
// are spread across it is still read in pieces, because the alternative is one
// allocation the size of whatever the store claims the object is.
func TestPlanBlobSpansStopsGrowingAtTheCap(t *testing.T) {
	const cap = 1000
	reads := []blobRead{
		{index: 0, ref: ref("blob/a", 0, 400)},
		{index: 1, ref: ref("blob/a", 500, 400)},
		{index: 2, ref: ref("blob/a", 1000, 400)},
	}
	spans := planBlobSpans(reads, 1024, cap)
	if len(spans) != 2 {
		t.Fatalf("planned %d spans, want 2: %v", len(spans), spans)
	}
	for _, s := range spans {
		if s.length > cap {
			t.Errorf("span %v exceeds the %d-byte cap", s, cap)
		}
	}
}

// A single member larger than the cap is still read: the cap governs merging,
// not the read a caller would have issued anyway.
func TestPlanBlobSpansStillReadsAMemberLargerThanTheCap(t *testing.T) {
	spans := planBlobSpans([]blobRead{{index: 0, ref: ref("blob/a", 0, 5000)}}, 1024, 1000)
	if len(spans) != 1 || spans[0].length != 5000 {
		t.Fatalf("planned %v, want the whole 5000-byte member", spans)
	}
}

// Offset and Length are read off a store, so their sum is not to be trusted to
// fit. Two references that are individually only implausible combine, through
// the merge, into a span of *negative* length — which reaches make() in a
// backend's GetRange and takes the process down.
//
// The planner drops them instead. A dropped reference is still read, on its own,
// where an unreadable one is reported against the one file it belongs to.
func TestPlanBlobSpansDropsAReferenceItCannotAddUp(t *testing.T) {
	const gap = 1 << 20

	tests := []struct {
		name  string
		reads []blobRead
	}{
		{
			// The pair the merge turns negative: planned as one span, it comes
			// out at -9223372036854774810.
			name: "two members whose extents overflow",
			reads: []blobRead{
				{index: 0, ref: ref("blob/a", 1, math.MaxInt64)},
				{index: 1, ref: ref("blob/a", 1000, math.MaxInt64)},
			},
		},
		{
			name: "one member ending past the end of the number line",
			reads: []blobRead{
				{index: 0, ref: ref("blob/a", math.MaxInt64, 8)},
			},
		},
		{
			name: "an overflowing member beside an ordinary one",
			reads: []blobRead{
				{index: 0, ref: ref("blob/a", 0, 100)},
				{index: 1, ref: ref("blob/a", 64, math.MaxInt64)},
				{index: 2, ref: ref("blob/a", 200, 100)},
			},
		},
		{
			name: "a negative offset",
			reads: []blobRead{
				{index: 0, ref: ref("blob/a", -1, 100)},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spans := planBlobSpans(tc.reads, gap, restoreSpanBytes())
			for _, s := range spans {
				if s.length <= 0 {
					t.Errorf("planned a span of length %d, which no store can be asked for", s.length)
				}
				if s.offset < 0 || s.offset > math.MaxInt64-s.length {
					t.Errorf("planned span [%d,%d+%d) does not fit an int64", s.offset, s.offset, s.length)
				}
				for _, idx := range s.members {
					r := tc.reads[idx].ref
					if r.Offset < 0 || r.Length <= 0 || r.Offset > math.MaxInt64-r.Length {
						t.Errorf("span %v carries member %d, whose extent does not fit", s, idx)
					}
				}
			}
		})
	}

	// The sound references either side of a bad one are still planned, so one
	// unusable entry costs its own file and not its neighbours' coalescing.
	spans := planBlobSpans([]blobRead{
		{index: 0, ref: ref("blob/a", 0, 100)},
		{index: 1, ref: ref("blob/a", 64, math.MaxInt64)},
		{index: 2, ref: ref("blob/a", 100, 100)},
	}, gap, restoreSpanBytes())
	if len(spans) != 1 || spans[0].length != 200 || len(spans[0].members) != 2 {
		t.Fatalf("planned %v, want the two sound members merged into one 200-byte span", spans)
	}
}

// A member's bytes are sliced out of the span that covered it, and a reference
// the span does not cover is refused rather than read out of bounds.
func TestBlobSpanSliceIsBoundedByWhatWasRead(t *testing.T) {
	s := blobSpan{blob: "blob/a", offset: 10, length: 20}
	data := []byte("0123456789abcdefghij")

	got, err := s.slice(data, ref("blob/a", 14, 4))
	if err != nil {
		t.Fatalf("slice: %v", err)
	}
	if string(got) != "4567" {
		t.Errorf("slice = %q, want %q", got, "4567")
	}

	for _, bad := range []*hamt.BodyRef{
		ref("blob/a", 5, 4),   // before the span
		ref("blob/a", 28, 4),  // past the end of it
		ref("blob/a", 10, 21), // longer than what was read
	} {
		if _, err := s.slice(data, bad); err == nil {
			t.Errorf("slice accepted %+v, which the span does not cover", bad)
		}
	}
}

// rangeCountingStore records every ranged read, which is the unit an object
// store charges for and the thing coalescing exists to reduce.
//
// It also refuses ranged reads longer than maxRange, so a test can make the
// coalesced path fail while leaving the per-member path working.
type rangeCountingStore struct {
	store.ObjectStore
	maxRange int64

	mu     sync.Mutex
	ranges map[string]int
}

func newRangeCountingStore(s store.ObjectStore) *rangeCountingStore {
	return &rangeCountingStore{ObjectStore: s, ranges: map[string]int{}}
}

func (s *rangeCountingStore) GetRange(ctx context.Context, key string, offset, length int64) ([]byte, error) {
	s.mu.Lock()
	s.ranges[key]++
	s.mu.Unlock()
	if s.maxRange > 0 && length > s.maxRange {
		return nil, fmt.Errorf("ranged read of %d bytes refused", length)
	}
	data, err := s.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if offset < 0 || length < 0 || offset+length > int64(len(data)) {
		return nil, fmt.Errorf("range %d+%d is outside %s (%d bytes)", offset, length, key, len(data))
	}
	return append([]byte(nil), data[offset:offset+length]...), nil
}

// countUnder reports the ranged reads issued against keys under prefix.
func (s *rangeCountingStore) countUnder(prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	for key, c := range s.ranges {
		if strings.HasPrefix(key, prefix) {
			n += c
		}
	}
	return n
}

// v3ManyFileSource builds a tree of small files, which is what a real snapshot
// is mostly made of and what packs several hundred members into one blob.
func v3ManyFileSource(n int) (*MockSource, map[string][]byte) {
	src := NewMockSource()
	want := make(map[string][]byte, n)
	for i := range n {
		name := fmt.Sprintf("f%04d.txt", i)
		body := []byte(strings.Repeat(fmt.Sprintf("%d-", i), 40))
		src.AddFile(name, fmt.Sprintf("id-%04d", i), body)
		want[name] = body
	}
	return src, want
}

// The point of the layout is that files backed up together are packed
// together, so a restore that wants all of them should pay one request per
// blob rather than one per file.
func TestRestoreCoalescesTheReadsWithinABlob(t *testing.T) {
	ctx := context.Background()
	dest := NewMockStore()
	src, want := v3ManyFileSource(400)

	if _, err := NewBackupManager(v3Deps(dest), src).Run(ctx); err != nil {
		t.Fatalf("backup: %v", err)
	}
	blobs := dest.CountPrefix("blob/")
	if blobs == 0 {
		t.Fatal("backup wrote no blobs")
	}

	counting := newRangeCountingStore(dest)
	deps := v3Deps(dest)
	deps.BlobStore = counting

	root := t.TempDir()
	w, err := NewFSRestoreWriter(root)
	if err != nil {
		t.Fatal(err)
	}
	res, err := NewRestoreManager(deps).Run(ctx, w, "latest")
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if res.Errors != 0 {
		t.Fatalf("restore reported %d errors", res.Errors)
	}
	assertRestoredTree(t, root, want)

	// Exactly one ranged read per blob. It is the floor, and with every file
	// wanted and one batch to plan them in it is the ceiling too: the batch
	// sees the whole blob's worth of members before it issues anything.
	//
	// Asserted as equality rather than as an upper bound, because an upper
	// bound is also satisfied by *no* ranged reads at all — a restore that
	// regressed to whole-object Gets, or that stopped going through
	// deps.BlobStore, would restore every file correctly and pass.
	if got := counting.countUnder("blob/"); got != blobs {
		t.Errorf("restore issued %d ranged reads for %d blobs and %d files, want one per blob",
			got, blobs, len(want))
	}
}

// The sequential writer coalesces too. It writes its files one at a time —
// a zip is one stream with one open entry — but that is a constraint on the
// writing, not on the reading, and paying one request per file for a zip
// restore would be paying it for nothing.
func TestZipRestoreCoalescesItsReadsToo(t *testing.T) {
	ctx := context.Background()
	dest := NewMockStore()
	src, want := v3ManyFileSource(300)

	if _, err := NewBackupManager(v3Deps(dest), src).Run(ctx); err != nil {
		t.Fatalf("backup: %v", err)
	}
	blobs := dest.CountPrefix("blob/")

	counting := newRangeCountingStore(dest)
	deps := v3Deps(dest)
	deps.BlobStore = counting

	var buf bytes.Buffer
	if _, err := NewRestoreManager(deps).Run(ctx, NewZipRestoreWriter(&buf), "latest"); err != nil {
		t.Fatalf("restore: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, f := range zr.File {
		body, ok := want[f.Name]
		if !ok {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(body) {
			t.Fatalf("restored %s = %q, want %q", f.Name, got, body)
		}
		seen++
	}
	if seen != len(want) {
		t.Fatalf("zip holds %d of %d files", seen, len(want))
	}

	// One per blob, exactly, for the same reason the directory restore asserts
	// equality: an upper bound would also pass if no ranged read were issued
	// at all.
	if got := counting.countUnder("blob/"); got != blobs {
		t.Errorf("zip restore issued %d ranged reads for %d blobs and %d files, want one per blob",
			got, blobs, len(want))
	}
}

// A coalesced read that fails must not fail the restore. Every member falls
// back to the read it would have issued anyway, which is what keeps a broken
// blob costing the files inside it rather than the whole snapshot.
func TestRestoreFallsBackWhenACoalescedReadFails(t *testing.T) {
	ctx := context.Background()
	dest := NewMockStore()
	src, want := v3ManyFileSource(200)

	if _, err := NewBackupManager(v3Deps(dest), src).Run(ctx); err != nil {
		t.Fatalf("backup: %v", err)
	}

	// Refuse anything longer than a single member, which is exactly the reads
	// coalescing introduced.
	counting := newRangeCountingStore(dest)
	counting.maxRange = 200
	deps := v3Deps(dest)
	deps.BlobStore = counting

	root := t.TempDir()
	w, err := NewFSRestoreWriter(root)
	if err != nil {
		t.Fatal(err)
	}
	res, err := NewRestoreManager(deps).Run(ctx, w, "latest")
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if res.Errors != 0 {
		t.Fatalf("restore reported %d errors where every member was readable on its own", res.Errors)
	}
	assertRestoredTree(t, root, want)
}

// A blob that is gone costs the files inside it and nothing more. Coalescing
// reads together must not turn one unreadable object into a failed restore —
// the rest of the snapshot is still worth recovering, which is what restore
// promised before its reads were planned in batches.
func TestRestoreCountsAMissingBlobRatherThanFailing(t *testing.T) {
	ctx := context.Background()
	dest := NewMockStore()
	src, big := v3Source()

	if _, err := NewBackupManager(v3Deps(dest), src).Run(ctx); err != nil {
		t.Fatalf("backup: %v", err)
	}
	for key := range dest.Data {
		if strings.HasPrefix(key, "blob/") {
			if err := dest.Delete(ctx, key); err != nil {
				t.Fatal(err)
			}
			break
		}
	}

	root := t.TempDir()
	w, err := NewFSRestoreWriter(root)
	if err != nil {
		t.Fatal(err)
	}
	res, err := NewRestoreManager(v3Deps(dest)).Run(ctx, w, "latest")
	if err != nil {
		t.Fatalf("restore failed outright on a missing blob: %v", err)
	}
	if res.Errors == 0 {
		t.Fatal("a missing blob was restored without complaint")
	}
	// big.bin is chunked rather than blobbed, so it must have survived the
	// loss of the blob its neighbours lived in.
	assertRestoredTree(t, root, map[string][]byte{"big.bin": big})
	if res.FilesWritten != 1 {
		t.Fatalf("restore wrote %d files; only the chunked one should have survived", res.FilesWritten)
	}
}

func assertRestoredTree(t *testing.T, root string, want map[string][]byte) {
	t.Helper()
	for name, body := range want {
		got, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read restored %s: %v", name, err)
		}
		if string(got) != string(body) {
			t.Fatalf("restored %s = %q, want %q", name, got, body)
		}
	}
}
