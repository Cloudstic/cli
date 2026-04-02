package main

import (
	"encoding/base64"
	"encoding/json"
	"unicode/utf8"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/engine"
)

type forgetSingleJSONResult struct {
	SnapshotID string                 `json:"snapshot_id"`
	Prune      *cloudstic.PruneResult `json:"prune,omitempty"`
}

type forgetPolicyJSONResult struct {
	DryRun bool                       `json:"dry_run"`
	Groups []engine.PolicyGroupResult `json:"groups"`
	Prune  *cloudstic.PruneResult     `json:"prune,omitempty"`
}

type breakLockJSONResult struct {
	Locks []*cloudstic.RepoLock `json:"locks"`
}

type keyPasswordJSONResult struct {
	Changed bool `json:"changed"`
}

type recoveryKeyJSONResult struct {
	RecoveryKey string `json:"recovery_key"`
}

type catJSONResult struct {
	Key      string `json:"key"`
	Data     any    `json:"data"`
	Encoding string `json:"encoding,omitempty"`
}

func (r *runner) writeJSON(v any) int {
	enc := json.NewEncoder(r.out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return r.fail("Failed to write JSON output: %v", err)
	}
	return 0
}

func makeForgetPolicyJSONResult(result *cloudstic.PolicyResult, dryRun bool) *forgetPolicyJSONResult {
	if result == nil {
		return nil
	}
	return &forgetPolicyJSONResult{
		DryRun: dryRun,
		Groups: result.Groups,
		Prune:  result.Prune,
	}
}

func makeCatJSONResults(results []*cloudstic.CatResult) []catJSONResult {
	items := make([]catJSONResult, 0, len(results))
	for _, result := range results {
		items = append(items, makeCatJSONResult(result))
	}
	return items
}

func makeCatJSONResult(result *cloudstic.CatResult) catJSONResult {
	item := catJSONResult{Key: result.Key}
	var decoded any
	if err := json.Unmarshal(result.Data, &decoded); err == nil {
		item.Data = decoded
		return item
	}
	if utf8.Valid(result.Data) {
		item.Data = string(result.Data)
		return item
	}
	item.Data = base64.StdEncoding.EncodeToString(result.Data)
	item.Encoding = "base64"
	return item
}

func (r *runner) failJSONFlagConflict(flagA, flagB string) int {
	return r.fail("%s cannot be combined with %s", flagA, flagB)
}
