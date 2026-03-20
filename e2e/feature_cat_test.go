package e2e

import (
	"strings"
	"testing"
)

// TestCLI_Feature_CatConfig verifies that `cloudstic cat config` returns valid
// JSON with expected fields after repository init.
func TestCLI_Feature_CatConfig(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "cat_config",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			r := h.MustInitEncrypted()

			var cfg map[string]interface{}
			r.Cat("-json", "config").MustUnmarshalJSON(&cfg)

			// An encrypted repo must declare encrypted: true.
			encrypted, ok := cfg["encrypted"]
			if !ok {
				t.Error("cat config: missing 'encrypted' field")
			}
			if enc, _ := encrypted.(bool); !enc {
				t.Errorf("cat config: expected encrypted=true, got %v", encrypted)
			}
		},
	})
}

// TestCLI_Feature_CatIndexLatest verifies that after a backup, `cat index/latest`
// returns a JSON object pointing to a snapshot.
// The index/latest object has a "latest_snapshot" field (not "ref").
func TestCLI_Feature_CatIndexLatest(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "cat_index_latest",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			r := h.WithFile("file.txt", "content").MustInitEncrypted()
			r.Backup()

			var idx map[string]interface{}
			out := r.Cat("-json", "index/latest").MustUnmarshalJSON(&idx).Raw()

			// The field name is "latest_snapshot" (full snapshot key).
			ref, ok := idx["latest_snapshot"]
			if !ok {
				t.Errorf("cat index/latest: missing 'latest_snapshot' field; got keys: %v\nraw output:\n%s", mapKeys(idx), out)
				return
			}
			refStr, _ := ref.(string)
			if !strings.HasPrefix(refStr, "snapshot/") {
				t.Errorf("cat index/latest: expected 'latest_snapshot' to start with 'snapshot/', got %q", refStr)
			}
		},
	})
}

func mapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
