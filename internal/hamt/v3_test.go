package hamt

import (
	"bytes"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/cloudstic/cli/internal/core"
)

// v3TestTree returns a format-v3 tree over a fresh in-memory store.
func v3TestTree() (*Tree, *inMemoryStore) {
	s := newInMemoryStore()
	return NewTree(s, WithFormatV3()), s
}

func testPayload(i int) *Payload {
	return &Payload{
		Meta:   fmt.Appendf(nil, `{"fileId":"file-%d","size":%d}`, i, i*10),
		Size:   int64(i * 10),
		Inline: fmt.Appendf(nil, "content-%d", i),
	}
}

func routingKeyFor(i int) string {
	return core.ComputeHash(fmt.Appendf(nil, "route-%d", i))
}

func TestV3PayloadRoundTrip(t *testing.T) {
	tree, store := v3TestTree()
	tx := tree.Edit("")

	const n = 100
	for i := range n {
		key := fmt.Sprintf("file-%d", i)
		value := fmt.Sprintf("filemeta/%064d", i)
		p := testPayload(i)
		if i%3 == 0 {
			// Every third entry is "chunked" rather than inline.
			p.Inline = nil
			p.Chunks = []string{fmt.Sprintf("chunk/%064d", i), fmt.Sprintf("chunk/%064d", i+1)}
		}
		if err := tx.InsertWithPayload(ctx, routingKeyFor(i), key, value, p); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	root, err := tx.Commit(ctx)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Every stored node must be in the binary encoding.
	for ref, data := range store.data {
		if !isV3NodeData(data) {
			t.Fatalf("node %s is not v3-encoded (starts with %q)", ref, data[:1])
		}
	}

	// Re-open through a fresh tree so nothing is served from the write path's
	// cache, and read every entry back.
	fresh := NewTree(store, WithFormatV3())
	for i := range n {
		key := fmt.Sprintf("file-%d", i)
		value, p, err := fresh.LookupEntry(ctx, root, routingKeyFor(i), key)
		if err != nil {
			t.Fatalf("lookup %s: %v", key, err)
		}
		if want := fmt.Sprintf("filemeta/%064d", i); value != want {
			t.Fatalf("lookup %s: value %q, want %q", key, value, want)
		}
		if p == nil {
			t.Fatalf("lookup %s: nil payload", key)
		}
		if !bytes.Equal(p.Meta, testPayload(i).Meta) {
			t.Fatalf("lookup %s: meta %q", key, p.Meta)
		}
		if p.Size != int64(i*10) {
			t.Fatalf("lookup %s: size %d", key, p.Size)
		}
		if i%3 == 0 {
			if p.Inline != nil || len(p.Chunks) != 2 {
				t.Fatalf("lookup %s: chunked entry came back inline=%v chunks=%v", key, p.Inline, p.Chunks)
			}
		} else if !bytes.Equal(p.Inline, fmt.Appendf(nil, "content-%d", i)) {
			t.Fatalf("lookup %s: inline %q", key, p.Inline)
		}
	}

	// WalkEntries must deliver every payload.
	seen := 0
	err = fresh.WalkEntries(ctx, root, func(key, value string, p *Payload) error {
		if p == nil {
			return fmt.Errorf("entry %s has no payload", key)
		}
		seen++
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if seen != n {
		t.Fatalf("walk saw %d entries, want %d", seen, n)
	}
}

func TestV3RootIsInsertionOrderIndependent(t *testing.T) {
	build := func(order []int) string {
		tree, _ := v3TestTree()
		tx := tree.Edit("")
		for _, i := range order {
			key := fmt.Sprintf("file-%d", i)
			if err := tx.InsertWithPayload(ctx, routingKeyFor(i), key, "filemeta/"+key, testPayload(i)); err != nil {
				t.Fatalf("insert: %v", err)
			}
		}
		root, err := tx.Commit(ctx)
		if err != nil {
			t.Fatalf("commit: %v", err)
		}
		return root
	}

	order := make([]int, 200)
	for i := range order {
		order[i] = i
	}
	forward := build(order)
	rand.New(rand.NewSource(1)).Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
	shuffled := build(order)

	if forward != shuffled {
		t.Fatalf("same content, different roots: %s vs %s", forward, shuffled)
	}
}

func TestV3LeavesSplitOnByteBudget(t *testing.T) {
	tree, store := v3TestTree()
	tx := tree.Edit("")

	// Four leaf budgets' worth of 64 KB inline entries: far over one leaf's
	// budget, far under maxLeafEntriesV3, so only the byte rule can split it.
	// Sized from the budget rather than at a fixed count, because what this
	// asserts is that the rule fires — not where the budget happens to sit.
	big := bytes.Repeat([]byte("x"), 64*1024)
	n := 4 * leafSplitBytesV3 / len(big)
	for i := range n {
		p := &Payload{Meta: fmt.Appendf(nil, `{"i":%d}`, i), Inline: big, Size: int64(len(big))}
		if err := tx.InsertWithPayload(ctx, routingKeyFor(i), fmt.Sprintf("file-%d", i), "v", p); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	root, err := tx.Commit(ctx)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	var leaves, oversized int
	for _, data := range store.data {
		if !isV3NodeData(data) || data[len(nodeMagicV3)] != nodeKindLeafV3 {
			continue
		}
		leaves++
		// A split leaf may exceed the budget by at most one entry's size.
		if len(data) > leafSplitBytesV3+2*len(big) {
			oversized++
		}
	}
	if leaves < 4 {
		t.Fatalf("expected the byte budget to split into several leaves, got %d", leaves)
	}
	if oversized > 0 {
		t.Fatalf("%d leaves grossly exceed the split budget", oversized)
	}

	// And the tree still reads correctly after splitting.
	value, p, err := tree.LookupEntry(ctx, root, routingKeyFor(7), "file-7")
	if err != nil || value != "v" || p == nil || !bytes.Equal(p.Inline, big) {
		t.Fatalf("lookup after split: value=%q payload=%v err=%v", value, p != nil, err)
	}
}

func TestV3DiffCarriesPayloads(t *testing.T) {
	tree, _ := v3TestTree()

	tx := tree.Edit("")
	for i := range 10 {
		if err := tx.InsertWithPayload(ctx, routingKeyFor(i), fmt.Sprintf("file-%d", i),
			fmt.Sprintf("old-%d", i), testPayload(i)); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	root1, err := tx.Commit(ctx)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	tx2 := tree.Edit(root1)
	// Modify 3, delete 5, add 100.
	if err := tx2.InsertWithPayload(ctx, routingKeyFor(3), "file-3", "new-3", testPayload(103)); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := tx2.Delete(ctx, routingKeyFor(5), "file-5"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := tx2.InsertWithPayload(ctx, routingKeyFor(100), "file-100", "new-100", testPayload(100)); err != nil {
		t.Fatalf("insert: %v", err)
	}
	root2, err := tx2.Commit(ctx)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	got := map[string]DiffEntry{}
	if err := tree.Diff(ctx, root1, root2, func(d DiffEntry) error {
		got[d.Key] = d
		return nil
	}); err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("diff reported %d entries, want 3: %v", len(got), got)
	}

	mod := got["file-3"]
	if mod.OldPayload == nil || mod.NewPayload == nil {
		t.Fatalf("modified entry missing payloads: %+v", mod)
	}
	if !bytes.Equal(mod.NewPayload.Meta, testPayload(103).Meta) {
		t.Fatalf("modified entry's new payload: %q", mod.NewPayload.Meta)
	}
	del := got["file-5"]
	if del.OldPayload == nil || del.NewValue != "" {
		t.Fatalf("deleted entry: %+v", del)
	}
	if !bytes.Equal(del.OldPayload.Meta, testPayload(5).Meta) {
		t.Fatalf("deleted entry's old payload: %q", del.OldPayload.Meta)
	}
	add := got["file-100"]
	if add.NewPayload == nil || add.OldValue != "" {
		t.Fatalf("added entry: %+v", add)
	}
}

func TestV3TreeDecodesV2Nodes(t *testing.T) {
	// Node *decoding* is format-agnostic — load sniffs each node's encoding —
	// so a v3-mode tree can traverse v2-written nodes and sees their entries
	// with nil payloads.
	//
	// Routing is not: the arity a tree was built with decides where an entry
	// lives, so a routed Lookup only works at the shape that built the tree.
	// That costs nothing in practice because a repository is entirely one
	// format and the client takes the shape from its recorded version — but
	// it is why this asserts a walk rather than a lookup.
	s := newInMemoryStore()
	v2 := NewTree(s)
	tx := v2.Edit("")
	for i := range 50 {
		if err := tx.Insert(ctx, routingKeyFor(i), fmt.Sprintf("file-%d", i), fmt.Sprintf("val-%d", i)); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	root, err := tx.Commit(ctx)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	v3 := NewTree(s, WithFormatV3())
	seen := 0
	err = v3.WalkEntries(ctx, root, func(key, value string, p *Payload) error {
		seen++
		if p != nil {
			return fmt.Errorf("v2 entry %s came back with a payload", key)
		}
		if !strings.HasPrefix(value, "val-") {
			return fmt.Errorf("entry %s has value %q", key, value)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("v3 tree walking v2 nodes: %v", err)
	}
	if seen != 50 {
		t.Fatalf("v3 tree walking v2 nodes saw %d entries, want 50", seen)
	}
}

func TestV3DecodeRejectsCorruption(t *testing.T) {
	tree, store := v3TestTree()
	tx := tree.Edit("")
	if err := tx.InsertWithPayload(ctx, routingKeyFor(1), "k", "v", testPayload(1)); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	for ref, data := range store.data {
		if !strings.HasPrefix(ref, nodePrefix) {
			continue
		}
		// Truncation must fail decoding, not panic. The store-level hash check
		// is bypassed on purpose by decoding directly.
		for cut := len(nodeMagicV3) + 1; cut < len(data); cut += 7 {
			if _, err := decodeNodeV3(data[:cut]); err == nil {
				t.Fatalf("truncated node at %d bytes decoded without error", cut)
			}
		}
	}
}

// The tuning overrides exist so a sweep costs a run rather than a rebuild
// (#524, #525). They must default to the constants and reject nonsense rather
// than failing a backup over a typo.
func TestV3TuningOverrides(t *testing.T) {
	if got := v3LeafSplitBytes(); got != leafSplitBytesV3 {
		t.Errorf("unset leaf budget = %d, want the constant %d", got, leafSplitBytesV3)
	}
	if got, want := v3NodeCacheBytes(), nodeCacheLeaves*leafSplitBytesV3; got != want {
		t.Errorf("unset cache budget = %d, want %d", got, want)
	}

	// Overriding the leaf budget alone must move the cache with it: the cache
	// is sized in leaves, so holding its bytes fixed would silently change how
	// many leaves it holds.
	t.Setenv(envLeafSplitBytesV3, "1048576")
	if got := v3LeafSplitBytes(); got != 1048576 {
		t.Errorf("overridden leaf budget = %d", got)
	}
	if got, want := v3NodeCacheBytes(), nodeCacheLeaves*1048576; got != want {
		t.Errorf("cache budget = %d, want %d — it must track the leaf budget", got, want)
	}

	// An explicit cache override wins over the derived value.
	t.Setenv(envNodeCacheBytesV3, "12345678")
	if got := v3NodeCacheBytes(); got != 12345678 {
		t.Errorf("explicit cache budget = %d", got)
	}

	for _, bad := range []string{"", "0", "-1", "banana", "8MB"} {
		t.Setenv(envLeafSplitBytesV3, bad)
		if got := v3LeafSplitBytes(); got != leafSplitBytesV3 {
			t.Errorf("leaf budget %q = %d, want the constant: a bad knob must not change behaviour", bad, got)
		}
	}
}

// A leaf budget set by the environment must actually change how leaves split,
// or the sweep it exists for measures nothing.
func TestV3LeafBudgetOverrideChangesSplitting(t *testing.T) {
	count := func(budget string) int {
		t.Setenv(envLeafSplitBytesV3, budget)
		tree, store := v3TestTree()
		tx := tree.Edit("")
		body := bytes.Repeat([]byte("x"), 32*1024)
		for i := range 64 {
			p := &Payload{Meta: fmt.Appendf(nil, `{"i":%d}`, i), Inline: body, Size: int64(len(body))}
			if err := tx.InsertWithPayload(ctx, routingKeyFor(i), fmt.Sprintf("f-%d", i), "v", p); err != nil {
				t.Fatalf("insert: %v", err)
			}
		}
		if _, err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
		var leaves int
		for _, data := range store.data {
			if isV3NodeData(data) && data[len(nodeMagicV3)] == nodeKindLeafV3 {
				leaves++
			}
		}
		return leaves
	}

	small, large := count("262144"), count("8388608")
	if small <= large {
		t.Errorf("a 256 KB budget produced %d leaves and an 8 MB budget %d; "+
			"the smaller budget must split more", small, large)
	}
}
