package engine

import (
	"context"
	"encoding/json"

	"github.com/cloudstic/cli/pkg/store"
)

const contentCatalogKey = "index/content"

// ContentCatalog maps content refs ("content/<hash>") to their chunk refs.
type ContentCatalog map[string][]string

func LoadContentCache(s store.ObjectStore) ContentCatalog {
	data, err := s.Get(context.Background(), contentCatalogKey)
	if err != nil {
		return ContentCatalog{}
	}
	var cat ContentCatalog
	if err := json.Unmarshal(data, &cat); err != nil {
		return ContentCatalog{}
	}
	debugf("content cache loaded: %d entries", len(cat))
	return cat
}

func SaveContentCache(s store.ObjectStore, cat ContentCatalog) error {
	if len(cat) == 0 {
		return nil
	}
	data, err := json.Marshal(cat)
	if err != nil {
		return err
	}
	debugf("content cache saved: %d entries", len(cat))
	return s.Put(context.Background(), contentCatalogKey, data)
}
