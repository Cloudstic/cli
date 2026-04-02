package e2e

import (
	"strings"
	"testing"
)

type catJSONItem struct {
	Key  string                 `json:"key"`
	Data map[string]interface{} `json:"data"`
}

// TestCLI_Feature_CatConfig verifies that `cloudstic cat config -json`
// returns a structured JSON array containing the config object.
func TestCLI_Feature_CatConfig(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "cat_config",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			r := h.MustInitEncrypted()

			var items []catJSONItem
			out := r.Cat("-json", "config").MustUnmarshalJSON(&items).Raw()
			if len(items) != 1 {
				t.Fatalf("cat config: expected 1 result, got %d\noutput:\n%s", len(items), out)
			}
			if items[0].Key != "config" {
				t.Fatalf("cat config: key = %q, want %q", items[0].Key, "config")
			}

			encrypted, ok := items[0].Data["encrypted"]
			if !ok {
				t.Fatal("cat config: missing 'encrypted' field")
			}
			if enc, _ := encrypted.(bool); !enc {
				t.Fatalf("cat config: expected encrypted=true, got %v", encrypted)
			}
		},
	})
}

// TestCLI_Feature_CatIndexLatest verifies that after a backup,
// `cloudstic cat index/latest -json` returns a structured JSON array
// containing an object with a latest snapshot reference.
func TestCLI_Feature_CatIndexLatest(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "cat_index_latest",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			r := h.WithFile("file.txt", "content").MustInitEncrypted()
			r.Backup()

			var items []catJSONItem
			out := r.Cat("-json", "index/latest").MustUnmarshalJSON(&items).Raw()
			if len(items) != 1 {
				t.Fatalf("cat index/latest: expected 1 result, got %d\noutput:\n%s", len(items), out)
			}
			if items[0].Key != "index/latest" {
				t.Fatalf("cat index/latest: key = %q, want %q", items[0].Key, "index/latest")
			}

			ref, ok := items[0].Data["latest_snapshot"]
			if !ok {
				t.Fatalf("cat index/latest: missing 'latest_snapshot' field\noutput:\n%s", out)
			}
			refStr, _ := ref.(string)
			if !strings.HasPrefix(refStr, "snapshot/") {
				t.Fatalf("cat index/latest: expected latest_snapshot to start with 'snapshot/', got %q", refStr)
			}
		},
	})
}
