package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/internal/core"
	"github.com/cloudstic/cli/internal/engine"
)

// ---------------------------------------------------------------------------
// Argument handling
// ---------------------------------------------------------------------------

func TestBuildFindQuery_PositionalPatternRoutesByShape(t *testing.T) {
	// The positional is shorthand: a pattern with a separator constrains the
	// full path, one without it constrains the basename.
	for _, tc := range []struct {
		pattern  string
		wantName string
		wantPath string
	}{
		{"*.pdf", "*.pdf", ""},
		{"Documents/**/report.pdf", "", "Documents/**/report.pdf"},
	} {
		q := queryFromArgs(t, &findArgs{globalFlags: newTestGlobalFlags(), pattern: tc.pattern})
		if q.Name != tc.wantName || q.Path != tc.wantPath {
			t.Errorf("pattern %q routed to name=%q path=%q, want name=%q path=%q",
				tc.pattern, q.Name, q.Path, tc.wantName, tc.wantPath)
		}
	}
}

func TestBuildFindQuery_PositionalConflictsWithExplicitPattern(t *testing.T) {
	a := &findArgs{globalFlags: newTestGlobalFlags(), pattern: "*.pdf", name: "*.txt"}
	if _, err := buildFindQuery(a); err == nil {
		t.Fatal("want an error when the positional and -name are both given")
	}
}

func TestBuildFindQuery_TypeAcceptsFindVocabulary(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want core.FileType
	}{
		{"f", core.FileTypeFile},
		{"file", core.FileTypeFile},
		{"d", core.FileTypeFolder},
		{"folder", core.FileTypeFolder},
	} {
		q := queryFromArgs(t, &findArgs{globalFlags: newTestGlobalFlags(), fileType: tc.raw})
		if q.Type != tc.want {
			t.Errorf("-type %q = %q, want %q", tc.raw, q.Type, tc.want)
		}
	}

	a := &findArgs{globalFlags: newTestGlobalFlags(), fileType: "symlink"}
	if _, err := buildFindQuery(a); err == nil {
		t.Fatal("want an error for an unsupported -type")
	}
}

func TestBuildFindQuery_SizeUsesFindSuffixSyntax(t *testing.T) {
	q := queryFromArgs(t, &findArgs{globalFlags: newTestGlobalFlags(), size: "+10M"})
	if q.Size == nil || q.Size.Op != cloudstic.SizeAtLeast || q.Size.Bytes != 10<<20 {
		t.Fatalf("-size +10M parsed to %+v", q.Size)
	}

	a := &findArgs{globalFlags: newTestGlobalFlags(), size: "10Q"}
	if _, err := buildFindQuery(a); err == nil {
		t.Fatal("want an error for an unknown size suffix")
	}
}

func TestBuildFindQuery_SnapshotAndFileTimeSelectorsStaySeparate(t *testing.T) {
	// -since/-until select snapshots; -newer/-older select files. The two
	// vocabularies collide easily, so this pins that they land in distinct
	// fields rather than one silently overwriting the other.
	q := queryFromArgs(t, &findArgs{
		globalFlags: newTestGlobalFlags(),
		name:        "*.txt",
		since:       "2026-01-01",
		until:       "2026-06-30",
		newer:       "7d",
		older:       "1y",
	})
	if q.Since != "2026-01-01" || q.Until != "2026-06-30" {
		t.Errorf("snapshot window = %q..%q", q.Since, q.Until)
	}
	if q.Newer != "7d" || q.Older != "1y" {
		t.Errorf("file mtime window = %q..%q", q.Newer, q.Older)
	}
}

// queryFromArgs builds the query a set of flags describes, failing the test if
// they are invalid.
func queryFromArgs(t *testing.T, a *findArgs) engine.FindQuery {
	t.Helper()
	q, err := buildFindQuery(a)
	if err != nil {
		t.Fatalf("buildFindQuery: %v", err)
	}
	return q
}

// ---------------------------------------------------------------------------
// Command flow
// ---------------------------------------------------------------------------

func TestRunFind_JSONEmitsResultAndKeepsWarningsOnStderr(t *testing.T) {
	var out, errOut strings.Builder
	stub := &stubClient{findResult: &cloudstic.FindResult{
		SnapshotsSearched: 3,
		GroupedBy:         "file",
		Warnings:          []string{"this -path pattern offers no basename prefilter"},
		Matches:           []cloudstic.FileMatch{sampleFindMatch()},
	}}
	g := newTestGlobalFlags()
	g.json = true
	r := &runner{out: &out, errOut: &errOut, client: stub}

	if code := runFind(r, context.Background(), &findArgs{globalFlags: g, pattern: "*.kdbx"}, clientConfig{}); code != 0 {
		t.Fatalf("exit code = %d", code)
	}

	var decoded cloudstic.FindResult
	if err := json.Unmarshal([]byte(out.String()), &decoded); err != nil {
		t.Fatalf("stdout is not valid JSON (%v): %s", err, out.String())
	}
	if len(decoded.Matches) != 1 {
		t.Errorf("decoded %d matches, want 1", len(decoded.Matches))
	}
	if !strings.Contains(errOut.String(), "prefilter") {
		t.Errorf("warning missing from stderr: %q", errOut.String())
	}
	// The warning belongs in the JSON payload as a field, but its human-readable
	// rendering must not be interleaved with it — that is what would break a
	// consumer piping stdout into a parser.
	if strings.Contains(out.String(), "warning: ") {
		t.Errorf("the rendered warning line contaminated the JSON on stdout: %s", out.String())
	}
	if len(decoded.Warnings) != 1 {
		t.Errorf("the JSON payload should still carry the warning, got %v", decoded.Warnings)
	}
}

func TestRunFind_NoMatchesSucceeds(t *testing.T) {
	// Exit 0 with no matches, consistent with list on an empty repository and
	// with find(1). Scripts distinguish the cases by reading the JSON.
	var out, errOut strings.Builder
	stub := &stubClient{findResult: &cloudstic.FindResult{SnapshotsSearched: 2, Elapsed: "1ms"}}
	r := &runner{out: &out, errOut: &errOut, client: stub}

	if code := runFind(r, context.Background(), &findArgs{globalFlags: newTestGlobalFlags(), pattern: "nope"}, clientConfig{}); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "No matches") {
		t.Errorf("output = %q", out.String())
	}
}

func TestRunFind_InvalidArgumentsFailBeforeOpeningTheRepository(t *testing.T) {
	var out, errOut strings.Builder
	stub := &stubClient{}
	r := &runner{out: &out, errOut: &errOut, client: stub}

	code := runFind(r, context.Background(), &findArgs{globalFlags: newTestGlobalFlags(), size: "nonsense"}, clientConfig{})
	if code == 0 {
		t.Fatal("want a non-zero exit for an unparseable -size")
	}
	if stub.findCalled {
		t.Error("the client was called despite an argument error")
	}
}

// ---------------------------------------------------------------------------
// Presentation
// ---------------------------------------------------------------------------

func TestPrintFindResultGolden(t *testing.T) {
	var buf strings.Builder
	printFindResult(&buf, sampleFindResult())
	assertGolden(t, "print_find_result", buf.String())
}

func TestPrintFindResult_NoMatchesGolden(t *testing.T) {
	var buf strings.Builder
	printFindResult(&buf, &cloudstic.FindResult{SnapshotsSearched: 31, Elapsed: "1.8s", GroupedBy: "file"})
	assertGolden(t, "print_find_no_matches", buf.String())
}

func TestPrintFindResult_ByContentGolden(t *testing.T) {
	var buf strings.Builder
	printFindResult(&buf, &cloudstic.FindResult{
		SnapshotsSearched: 4,
		Elapsed:           "42ms",
		GroupedBy:         "content",
		Matches: []cloudstic.FileMatch{{
			ContentHash: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
			Type:        core.FileTypeFile,
			Versions: []cloudstic.FileVersion{
				{
					Ref:  "filemeta/2c624232cdd221771294dfbb310aca000a0df6ac8b66b696d90ef06fdefb64a3",
					Name: "report.pdf", FileID: "f1", Size: 4 << 20, Mtime: 1_769_000_000,
					Paths:     []string{"Documents/report.pdf"},
					Snapshots: []cloudstic.SnapshotRef{{Ref: "snapshot/410b18a2c9e35f1a8d6b3c07e42fa19d5c8b6e2d1a0f9c8b7a6e5d4c3b2a1908", Seq: 4, Created: "2026-07-21T09:14:00Z"}},
					LastSeen:  "2026-07-21T09:14:00Z",
				},
				{
					Ref:  "filemeta/e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
					Name: "report.pdf", FileID: "f2", Size: 4 << 20, Mtime: 1_769_000_000,
					Paths:     []string{"Backup/report.pdf"},
					Snapshots: []cloudstic.SnapshotRef{{Ref: "snapshot/410b18a2c9e35f1a8d6b3c07e42fa19d5c8b6e2d1a0f9c8b7a6e5d4c3b2a1908", Seq: 4, Created: "2026-07-21T09:14:00Z"}},
					LastSeen:  "2026-07-21T09:14:00Z",
				},
			},
		}},
	})
	assertGolden(t, "print_find_by_content", buf.String())
}

func TestPrintFindResult_TruncationIsStated(t *testing.T) {
	var buf strings.Builder
	result := sampleFindResult()
	result.Truncated = true
	printFindResult(&buf, result)
	if !strings.Contains(buf.String(), "-max-results") {
		t.Errorf("a truncated result must say how to see the rest:\n%s", buf.String())
	}
}

func TestPrintFindResult_MultiParentPathsAreAllShown(t *testing.T) {
	var buf strings.Builder
	printFindResult(&buf, &cloudstic.FindResult{
		SnapshotsSearched: 1, Elapsed: "5ms", GroupedBy: "file",
		Matches: []cloudstic.FileMatch{{
			FileID: "drive-1", Type: core.FileTypeFile,
			Versions: []cloudstic.FileVersion{{
				Ref: "filemeta/9f86d081", Name: "spec.md", FileID: "drive-1", Size: 1024,
				Paths:     []string{"Work/spec.md", "Shared/spec.md"},
				Snapshots: []cloudstic.SnapshotRef{{Ref: "snapshot/aaaa1111", Created: "2026-07-21T09:14:00Z"}},
			}},
		}},
	})
	got := buf.String()
	if !strings.Contains(got, "Work/spec.md") || !strings.Contains(got, "at Shared/spec.md") {
		t.Errorf("both paths of a multi-parent entry must appear:\n%s", got)
	}
}

func sampleFindMatch() cloudstic.FileMatch {
	return cloudstic.FileMatch{
		FileID: "f1",
		Type:   core.FileTypeFile,
		Versions: []cloudstic.FileVersion{
			{
				Ref:    "filemeta/9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
				FileID: "f1", Name: "vault.kdbx", Size: 4_404_019, Mtime: 1_784_970_840,
				Paths: []string{"Documents/vault.kdbx"},
				Snapshots: []cloudstic.SnapshotRef{
					{Ref: "snapshot/410b18a2c9e35f1a8d6b3c07e42fa19d5c8b6e2d1a0f9c8b7a6e5d4c3b2a1908", Seq: 28, Created: "2026-07-21T09:14:00Z"},
					{Ref: "snapshot/4e5d5487", Seq: 22, Created: "2026-07-14T09:14:00Z"},
				},
				FirstSeen: "2026-07-14T09:14:00Z", LastSeen: "2026-07-21T09:14:00Z",
			},
			{
				Ref:    "filemeta/2c624232cdd221771294dfbb310aca000a0df6ac8b66b696d90ef06fdefb64a3",
				FileID: "f1", Name: "vault.kdbx", Size: 4_299_161, Mtime: 1_782_842_520,
				Paths: []string{"Documents/vault.kdbx"},
				Snapshots: []cloudstic.SnapshotRef{
					{Ref: "snapshot/7d793037", Seq: 21, Created: "2026-06-30T18:02:00Z"},
					{Ref: "snapshot/1b6453892", Seq: 9, Created: "2026-06-01T18:02:00Z"},
				},
				FirstSeen: "2026-06-01T18:02:00Z", LastSeen: "2026-06-30T18:02:00Z",
			},
		},
	}
}

func sampleFindResult() *cloudstic.FindResult {
	return &cloudstic.FindResult{
		SnapshotsSearched: 31,
		EntriesScanned:    1420,
		MetaFetched:       310,
		Elapsed:           "1.8s",
		GroupedBy:         "file",
		Matches:           []cloudstic.FileMatch{sampleFindMatch()},
	}
}
