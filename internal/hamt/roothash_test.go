package hamt

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The root ref of a HAMT is part of the on-disk contract, not an implementation
// detail. `newRoot == oldRoot` is what WithIgnoreEmptySnapshot tests to decide a
// backup was a no-op, and unchanged-subtree deduplication across snapshots works
// only because identical trees serialize to identical bytes and therefore
// identical refs.
//
// This test pins the roots produced by a set of fixed operation sequences. A
// refactor of this package must reproduce them exactly. If this test fails, the
// bytes written to repositories have changed: that is a repository format change
// governed by docs/compatibility.md, not something to regenerate away. Only
// re-run with -update when the format change is deliberate and the compatibility
// checklist has been followed.

var updateGolden = flag.Bool("update", false, "rewrite the root-hash golden file")

// op is a single tree mutation in a scenario.
type op struct {
	del      bool
	parentID string
	fileID   string
	value    string
}

func ins(parent, file, value string) op { return op{parentID: parent, fileID: file, value: value} }
func del(parent, file string) op        { return op{del: true, parentID: parent, fileID: file} }

// scenario is a named sequence of operations whose resulting root is pinned.
type scenario struct {
	name string
	ops  []op
	// v3 runs the scenario against a format-v3 tree, whose leaves carry
	// binary-encoded payloads. Without this the golden pinned only the v2 JSON
	// encoding, so every field, tag and flag of the v3 leaf could change
	// without a test noticing — and a v3 leaf encoding change rewrites every
	// root hash in every v3 repository just as surely.
	v3 bool
}

// v3Payload is a deterministic payload for scenario entry o.
//
// It exercises all three shapes a v3 entry takes, because they are separate
// branches of the encoder: metadata only (a folder), metadata plus a body
// reference, and metadata plus chunk refs. Cycling by index means the golden
// covers each without three scenarios.
func v3Payload(o op, i int) *Payload {
	p := &Payload{
		Meta: fmt.Appendf(nil, `{"v":%q}`, o.value),
		Size: int64(len(o.value)),
	}
	switch i % 3 {
	case 0:
		// Metadata only.
	case 1:
		p.Body = &BodyRef{
			Blob:   fmt.Sprintf("blob/%064d", i/8),
			Offset: int64(i%8) * 512,
			Length: 512,
			Total:  4096,
		}
	case 2:
		p.Chunks = []string{
			fmt.Sprintf("chunk/%064d", i),
			fmt.Sprintf("chunk/%064d", i+1),
		}
	}
	return p
}

func rootHashScenarios() []scenario {
	var flat []op
	for i := 0; i < 200; i++ {
		flat = append(flat, ins("", fmt.Sprintf("file-%04d", i), fmt.Sprintf("filemeta/ref-%04d", i)))
	}

	var affinity []op
	for d := 0; d < 10; d++ {
		for f := 0; f < 30; f++ {
			affinity = append(affinity, ins(
				fmt.Sprintf("dir-%02d", d),
				fmt.Sprintf("file-%02d-%03d", d, f),
				fmt.Sprintf("filemeta/ref-%02d-%03d", d, f),
			))
		}
	}

	// Grow past several splits, then delete half. Sixty entries survive, which
	// is more than a leaf holds, so the tree stays internal: this pins the
	// empty-slot and single-leaf-promotion paths without reaching the general
	// collapse below.
	var collapse []op
	for i := 0; i < 120; i++ {
		collapse = append(collapse, ins("", fmt.Sprintf("k%03d", i), fmt.Sprintf("filemeta/v%03d", i)))
	}
	for i := 0; i < 120; i += 2 {
		collapse = append(collapse, del("", fmt.Sprintf("k%03d", i)))
	}

	// The same growth, then deleted past the point where a leaf could hold what
	// remains. Twenty entries survive, so every internal node re-merges and the
	// tree becomes the single leaf a fresh build of those entries would produce.
	//
	// This is the scenario the canonical collapse exists for. Without it the
	// golden pins only trees too large to collapse, and the rule that decides
	// the shape of a shrinking tree would be unpinned.
	var collapseDeep []op
	for i := 0; i < 120; i++ {
		collapseDeep = append(collapseDeep, ins("", fmt.Sprintf("k%03d", i), fmt.Sprintf("filemeta/v%03d", i)))
	}
	for i := 20; i < 120; i++ {
		collapseDeep = append(collapseDeep, del("", fmt.Sprintf("k%03d", i)))
	}

	// Overwrites and deletes of absent keys must not perturb the root.
	var updates []op
	for i := 0; i < 60; i++ {
		updates = append(updates, ins("shared", fmt.Sprintf("u%02d", i), "filemeta/first"))
	}
	for i := 0; i < 60; i++ {
		updates = append(updates, ins("shared", fmt.Sprintf("u%02d", i), fmt.Sprintf("filemeta/second-%02d", i)))
	}
	updates = append(updates, del("shared", "absent"), del("other", "u00"))

	return []scenario{
		{name: "flat-200-no-parent", ops: flat},
		{name: "affinity-10-dirs-30-files", ops: affinity},
		{name: "insert-120-delete-even", ops: collapse},
		{name: "insert-120-delete-to-20", ops: collapseDeep},
		{name: "overwrite-and-absent-delete", ops: updates},
		// The same shapes again in format v3, whose leaves carry binary
		// payloads. A v3 leaf encoding change rewrites every root hash in
		// every v3 repository, exactly as a v2 node change does, so it is
		// pinned the same way.
		{name: "v3-flat-200-no-parent", ops: flat, v3: true},
		{name: "v3-affinity-10-dirs-30-files", ops: affinity, v3: true},
		{name: "v3-insert-120-delete-even", ops: collapse, v3: true},
	}
}

// runScenario applies ops in order and returns the final root ref.
func runScenario(t *testing.T, s scenario) string {
	t.Helper()
	var tree *Tree
	if s.v3 {
		tree = NewTree(newInMemoryStore(), WithFormatV3())
	} else {
		tree = NewTree(newInMemoryStore())
	}
	root := ""
	var err error
	for i, o := range s.ops {
		switch {
		case o.del:
			root, err = deleteCommit(tree, root, routingKey(o.parentID, o.fileID), o.fileID)
		case s.v3:
			root, err = insertPayloadCommit(tree, root, routingKey(o.parentID, o.fileID), o.fileID, o.value, v3Payload(o, i))
		default:
			root, err = insertCommit(tree, root, routingKey(o.parentID, o.fileID), o.fileID, o.value)
		}
		if err != nil {
			t.Fatalf("%s: op %d (%+v): %v", s.name, i, o, err)
		}
	}
	return root
}

func TestRootHashGolden(t *testing.T) {
	scenarios := rootHashScenarios()

	var lines []string
	for _, s := range scenarios {
		lines = append(lines, s.name+" "+runScenario(t, s))
	}
	got := strings.Join(lines, "\n") + "\n"

	path := filepath.Join("testdata", "root_hashes.golden")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Fatalf("HAMT root hashes changed — this is an on-disk format change.\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestRootHashIsOrderIndependent pins the other half of the contract: two trees
// holding the same entries must share a root regardless of insertion order, or
// unchanged-subtree deduplication silently stops working.
func TestRootHashIsOrderIndependent(t *testing.T) {
	forward := scenario{name: "forward"}
	backward := scenario{name: "backward"}
	for i := 0; i < 150; i++ {
		o := ins(fmt.Sprintf("dir-%d", i%7), fmt.Sprintf("f%03d", i), fmt.Sprintf("filemeta/v%03d", i))
		forward.ops = append(forward.ops, o)
		backward.ops = append([]op{o}, backward.ops...)
	}

	if a, b := runScenario(t, forward), runScenario(t, backward); a != b {
		t.Fatalf("insertion order changed the root: forward=%s backward=%s", a, b)
	}
}
