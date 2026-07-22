package hamt

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"
)

// A HAMT is, observably, a map. This test runs randomized operation sequences
// against the real Tree and against a plain map[string]string reference model,
// and asserts after every operation that the two agree on Lookup, on Walk, and
// on the Diff between the previous root and the current one.
//
// It is the behavioral half of the refactor safety net: TestRootHashGolden pins
// the bytes, this pins the semantics.

// model is the reference implementation: a map, plus the routing keys needed to
// query the real tree.
type model struct {
	values  map[string]string
	parents map[string]string
}

func newModel() *model {
	return &model{values: map[string]string{}, parents: map[string]string{}}
}

func (m *model) put(parentID, fileID, value string) {
	m.values[fileID] = value
	m.parents[fileID] = parentID
}

func (m *model) remove(fileID string) {
	delete(m.values, fileID)
	delete(m.parents, fileID)
}

func (m *model) clone() map[string]string {
	c := make(map[string]string, len(m.values))
	for k, v := range m.values {
		c[k] = v
	}
	return c
}

// diffModels returns the expected DiffEntry set between two model snapshots,
// keyed by entry key.
func diffModels(before, after map[string]string) map[string]DiffEntry {
	want := map[string]DiffEntry{}
	for k, oldV := range before {
		newV, ok := after[k]
		if !ok {
			want[k] = DiffEntry{Key: k, OldValue: oldV}
		} else if newV != oldV {
			want[k] = DiffEntry{Key: k, OldValue: oldV, NewValue: newV}
		}
	}
	for k, newV := range after {
		if _, ok := before[k]; !ok {
			want[k] = DiffEntry{Key: k, NewValue: newV}
		}
	}
	return want
}

func TestTreeMatchesMapModel(t *testing.T) {
	// A small key space relative to the operation count so that overwrites,
	// deletes of present keys, and deletes of absent keys all occur often.
	const (
		ops     = 600
		keySpce = 220
		dirs    = 9
	)

	for _, seed := range []int64{1, 2, 3, 20260722} {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			tree := NewTree(newInMemoryStore())
			m := newModel()

			root := ""
			var err error

			for i := 0; i < ops; i++ {
				before := m.clone()
				beforeRoot := root

				fileID := fmt.Sprintf("file-%03d", rng.Intn(keySpce))
				// Deleting must use the routing key the entry was inserted
				// under, so reuse the recorded parent when the key is present.
				parentID := fmt.Sprintf("dir-%d", rng.Intn(dirs))
				if p, ok := m.parents[fileID]; ok {
					parentID = p
				}

				if rng.Intn(100) < 65 {
					value := fmt.Sprintf("filemeta/v-%d-%d", i, rng.Intn(1000))
					root, err = tree.Insert(ctx, root, routingKey(parentID, fileID), fileID, value)
					if err != nil {
						t.Fatalf("op %d: Insert %s: %v", i, fileID, err)
					}
					m.put(parentID, fileID, value)
				} else {
					root, err = tree.Delete(ctx, root, routingKey(parentID, fileID), fileID)
					if err != nil {
						t.Fatalf("op %d: Delete %s: %v", i, fileID, err)
					}
					m.remove(fileID)
				}

				assertLookups(t, tree, root, m, i)
				assertWalk(t, tree, root, m, i)
				assertDiff(t, tree, beforeRoot, root, before, m.clone(), i)
			}
		})
	}
}

func assertLookups(t *testing.T, tree *Tree, root string, m *model, op int) {
	t.Helper()
	for k, want := range m.values {
		got, err := tree.Lookup(ctx, root, routingKey(m.parents[k], k), k)
		if err != nil {
			t.Fatalf("op %d: Lookup %s: %v", op, k, err)
		}
		if got != want {
			t.Fatalf("op %d: Lookup %s = %q, model says %q", op, k, got, want)
		}

		// LookupByKey must agree with Lookup without being told the routing key.
		byKey, err := tree.LookupByKey(ctx, root, k)
		if err != nil {
			t.Fatalf("op %d: LookupByKey %s: %v", op, k, err)
		}
		if byKey != want {
			t.Fatalf("op %d: LookupByKey %s = %q, model says %q", op, k, byKey, want)
		}
	}
}

func assertWalk(t *testing.T, tree *Tree, root string, m *model, op int) {
	t.Helper()
	seen := map[string]string{}
	err := tree.Walk(ctx, root, func(key, value string) error {
		if _, dup := seen[key]; dup {
			return fmt.Errorf("key %s visited twice", key)
		}
		seen[key] = value
		return nil
	})
	if err != nil {
		t.Fatalf("op %d: Walk: %v", op, err)
	}
	if len(seen) != len(m.values) {
		t.Fatalf("op %d: Walk saw %d entries, model has %d", op, len(seen), len(m.values))
	}
	for k, want := range m.values {
		if seen[k] != want {
			t.Fatalf("op %d: Walk gave %s=%q, model says %q", op, k, seen[k], want)
		}
	}
}

func assertDiff(t *testing.T, tree *Tree, oldRoot, newRoot string, before, after map[string]string, op int) {
	t.Helper()

	// An unchanged tree must produce an unchanged root, and vice versa.
	want := diffModels(before, after)
	if (oldRoot == newRoot) != (len(want) == 0) {
		t.Fatalf("op %d: root equality %v disagrees with model delta of %d entries",
			op, oldRoot == newRoot, len(want))
	}

	// Diff requires two existing roots: an empty root is not a tree it can
	// load, and every caller guards for it (see BackupManager.countRemoved).
	if oldRoot == "" || newRoot == "" {
		return
	}

	got := map[string]DiffEntry{}
	err := tree.Diff(ctx, oldRoot, newRoot, func(d DiffEntry) error {
		if _, dup := got[d.Key]; dup {
			return fmt.Errorf("key %s reported twice", d.Key)
		}
		got[d.Key] = d
		return nil
	})
	if err != nil {
		t.Fatalf("op %d: Diff: %v", op, err)
	}

	if len(got) != len(want) {
		t.Fatalf("op %d: Diff reported %v, model expects %v", op, sortedKeys(got), sortedKeys(want))
	}
	for k, w := range want {
		if got[k] != w {
			t.Fatalf("op %d: Diff for %s = %+v, model expects %+v", op, k, got[k], w)
		}
	}
}

func sortedKeys(m map[string]DiffEntry) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
