package engine

import (
	"context"
	"testing"
)

func TestContentCache_SaveAndLoad(t *testing.T) {
	s := NewMockStore()

	cat := ContentCatalog{
		"content/aaa": {"chunk/1", "chunk/2"},
		"content/bbb": nil,
	}
	if err := SaveContentCache(s, cat); err != nil {
		t.Fatal(err)
	}

	loaded := LoadContentCache(s)
	if len(loaded) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(loaded))
	}
	if len(loaded["content/aaa"]) != 2 {
		t.Errorf("expected 2 chunks for content/aaa, got %d", len(loaded["content/aaa"]))
	}
	if loaded["content/bbb"] != nil {
		t.Errorf("expected nil chunks for inline content, got %v", loaded["content/bbb"])
	}
}

func TestContentCache_EmptyNotSaved(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	if err := SaveContentCache(s, ContentCatalog{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, contentCatalogKey); err == nil {
		t.Error("empty catalog should not be persisted")
	}
}

func TestContentCache_CorruptedReturnsEmpty(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()
	_ = s.Put(ctx, contentCatalogKey, []byte("not json"))

	cat := LoadContentCache(s)
	if len(cat) != 0 {
		t.Errorf("corrupted cache should return empty, got %d entries", len(cat))
	}
}

func TestContentCache_MissingReturnsEmpty(t *testing.T) {
	s := NewMockStore()
	cat := LoadContentCache(s)
	if len(cat) != 0 {
		t.Errorf("missing cache should return empty, got %d entries", len(cat))
	}
}
