package main

import (
	"os"
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
)

func TestRunCat_SingleKey_JSON(t *testing.T) {
	os.Args = []string{"cloudstic", "cat", "config"}
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}, client: &stubClient{
		catResults: []*cloudstic.CatResult{
			{Key: "config", Data: []byte(`{"version":1}`)},
		},
	}}

	r.runCat()

	got := out.String()
	if !strings.Contains(got, `"version"`) {
		t.Errorf("expected pretty-printed JSON, got:\n%s", got)
	}
}

func TestRunCat_MultipleKeys_HeadersShown(t *testing.T) {
	os.Args = []string{"cloudstic", "cat", "config", "index/latest"}
	var errOut strings.Builder
	r := &runner{out: &strings.Builder{}, errOut: &errOut, client: &stubClient{
		catResults: []*cloudstic.CatResult{
			{Key: "config", Data: []byte(`{}`)},
			{Key: "index/latest", Data: []byte(`{}`)},
		},
	}}

	r.runCat()

	got := errOut.String()
	if !strings.Contains(got, "==> config <==") {
		t.Errorf("expected header for first key, got:\n%s", got)
	}
	if !strings.Contains(got, "==> index/latest <==") {
		t.Errorf("expected header for second key, got:\n%s", got)
	}
}

func TestRunCat_QuietMode_NoHeaders(t *testing.T) {
	os.Args = []string{"cloudstic", "cat", "--json", "config", "index/latest"}
	var errOut strings.Builder
	r := &runner{out: &strings.Builder{}, errOut: &errOut, client: &stubClient{
		catResults: []*cloudstic.CatResult{
			{Key: "config", Data: []byte(`{}`)},
			{Key: "index/latest", Data: []byte(`{}`)},
		},
	}}

	r.runCat()

	if strings.Contains(errOut.String(), "==>") {
		t.Errorf("quiet mode should not show headers, got:\n%s", errOut.String())
	}
}

func TestRunCat_RawMode(t *testing.T) {
	os.Args = []string{"cloudstic", "cat", "-raw", "config"}
	var out strings.Builder
	rawData := []byte("raw bytes here")
	r := &runner{out: &out, errOut: &strings.Builder{}, client: &stubClient{
		catResults: []*cloudstic.CatResult{{Key: "config", Data: rawData}},
	}}

	r.runCat()

	if out.String() != string(rawData) {
		t.Errorf("raw mode: expected %q, got %q", rawData, out.String())
	}
}

func TestRunCat_InvalidJSON_PrintsRaw(t *testing.T) {
	os.Args = []string{"cloudstic", "cat", "config"}
	var out strings.Builder
	r := &runner{out: &out, errOut: &strings.Builder{}, client: &stubClient{
		catResults: []*cloudstic.CatResult{{Key: "config", Data: []byte("not-json")}},
	}}

	r.runCat()

	if !strings.Contains(out.String(), "not-json") {
		t.Errorf("expected raw fallback output, got:\n%s", out.String())
	}
}
