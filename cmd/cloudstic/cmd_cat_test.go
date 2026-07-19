package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
)

func TestRunCat_SingleKey_JSON(t *testing.T) {
	args := []string{"config"}
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}, client: &stubClient{
		catResults: []*cloudstic.CatResult{
			{Key: "config", Data: []byte(`{"version":1}`)},
		},
	}}

	runCat(r.withArgs(args), context.Background())

	got := out.String()
	if !strings.Contains(got, `"version"`) {
		t.Errorf("expected pretty-printed JSON, got:\n%s", got)
	}
}

func TestRunCat_MultipleKeys_HeadersShown(t *testing.T) {
	args := []string{"config", "index/latest"}
	var errOut strings.Builder
	r := &runner{out: &strings.Builder{}, errOut: &errOut, client: &stubClient{
		catResults: []*cloudstic.CatResult{
			{Key: "config", Data: []byte(`{}`)},
			{Key: "index/latest", Data: []byte(`{}`)},
		},
	}}

	runCat(r.withArgs(args), context.Background())

	got := errOut.String()
	if !strings.Contains(got, "==> config <==") {
		t.Errorf("expected header for first key, got:\n%s", got)
	}
	if !strings.Contains(got, "==> index/latest <==") {
		t.Errorf("expected header for second key, got:\n%s", got)
	}
}

func TestRunCat_JSON_NoHeadersAndStructuredOutput(t *testing.T) {
	args := []string{"--json", "config", "index/latest"}
	var out strings.Builder
	var errOut strings.Builder
	r := &runner{out: &out, errOut: &errOut, client: &stubClient{
		catResults: []*cloudstic.CatResult{
			{Key: "config", Data: []byte(`{"version":1}`)},
			{Key: "index/latest", Data: []byte(`{}`)},
		},
	}}

	if exit := runCat(r.withArgs(args), context.Background()); exit != 0 {
		t.Fatalf("runCat() exit = %d, want 0", exit)
	}

	if strings.Contains(errOut.String(), "==>") {
		t.Errorf("json mode should not show headers, got:\n%s", errOut.String())
	}

	var got []map[string]any
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatalf("json unmarshal: %v\noutput:\n%s", err, out.String())
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0]["key"] != "config" {
		t.Fatalf("first key = %v, want config", got[0]["key"])
	}
}

func TestRunCat_RawMode(t *testing.T) {
	args := []string{"-raw", "config"}
	var out strings.Builder
	rawData := []byte("raw bytes here")
	r := &runner{out: &out, errOut: &strings.Builder{}, client: &stubClient{
		catResults: []*cloudstic.CatResult{{Key: "config", Data: rawData}},
	}}

	runCat(r.withArgs(args), context.Background())

	if out.String() != string(rawData) {
		t.Errorf("raw mode: expected %q, got %q", rawData, out.String())
	}
}

func TestRunCat_InvalidJSON_PrintsRaw(t *testing.T) {
	args := []string{"config"}
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}, client: &stubClient{
		catResults: []*cloudstic.CatResult{{Key: "config", Data: []byte("not-json")}},
	}}

	runCat(r.withArgs(args), context.Background())

	if !strings.Contains(out.String(), "not-json") {
		t.Errorf("expected raw fallback output, got:\n%s", out.String())
	}
}

func TestRunCat_JSONAndRawConflict(t *testing.T) {
	args := []string{"-json", "-raw", "config"}
	var errOut strings.Builder
	r := &runner{out: &strings.Builder{}, errOut: &errOut, client: &stubClient{}}

	if exit := runCat(r.withArgs(args), context.Background()); exit != 1 {
		t.Fatalf("runCat() exit = %d, want 1", exit)
	}
	if !strings.Contains(errOut.String(), "-json cannot be combined with -raw") {
		t.Fatalf("expected conflict error, got:\n%s", errOut.String())
	}
}
