package storelayer

import "testing"

// catalogOf builds a packCatalog from a plain map, for tests that describe a
// catalog literally.
func catalogOf(entries map[string]PackEntry) *packCatalog {
	c := newPackCatalog()
	for key, entry := range entries {
		c.Set(key, entry)
	}
	return c
}

// mustCatalogGet returns the entry for key, failing the test if it is absent.
func mustCatalogGet(t *testing.T, c *packCatalog, key string) PackEntry {
	t.Helper()
	entry, ok := c.Get(key)
	if !ok {
		t.Fatalf("catalog has no entry for %s", key)
	}
	return entry
}
