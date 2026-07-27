package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCLI_Feature_Find covers the promise the command exists for: locating a
// file across snapshots and reporting its history as versions rather than as one
// row per (snapshot × path).
//
// Find is metadata-only, so a local store is sufficient here; the layered store
// path gets its own test below.
func TestCLI_Feature_Find(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "find_across_snapshots",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			r := h.
				WithFile("docs/vault.kdbx", "version one").
				WithFile("docs/notes.txt", "notes").
				MustInitEncrypted()

			// Three snapshots: unchanged, then edited.
			r.Backup()
			r.Backup()
			r.WithFile("docs/vault.kdbx", "version two, which is longer").Backup()

			result := r.FindJSON("vault.kdbx").MustHaveSearchedSnapshots(3)

			// One file, two versions — not three rows, and not one per snapshot.
			result.MustMatchPaths("docs/vault.kdbx")
			result.MustHaveVersions("docs/vault.kdbx", 2)

			// The unchanged pair collapses: the older version is credited with
			// both snapshots that held it, because they share one metadata
			// object rather than merely looking alike.
			result.MustHaveVersionInSnapshots("docs/vault.kdbx", 0, 1)
			result.MustHaveVersionInSnapshots("docs/vault.kdbx", 1, 2)

			// Newest first, and the sizes match what was written.
			versions := result.MustMatchPath("docs/vault.kdbx").Versions
			if versions[0].Size <= versions[1].Size {
				t.Errorf("expected the newest version first; got sizes %d then %d",
					versions[0].Size, versions[1].Size)
			}
		},
	})
}

// TestCLI_Feature_FindLocatesDeletedFileAndPrintsAWorkingRestore is the payoff
// case. A file that is no longer in the latest snapshot is invisible to `ls`,
// which is exactly when `find` earns its place.
//
// It also runs the restore command find printed, verbatim. A hint that has to
// be edited before it works is worse than no hint, and only an end-to-end run
// can catch a flag name that does not exist.
func TestCLI_Feature_FindLocatesDeletedFileAndPrintsAWorkingRestore(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "find_deleted_file",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			const secret = "the contents worth recovering"
			r := h.
				WithFile("docs/keep.txt", "still here").
				WithFile("docs/deleted-later.txt", secret).
				MustInitEncrypted()
			r.Backup()

			r.RemoveFile("docs/deleted-later.txt")
			r.Backup()

			// Gone from the latest snapshot, so ls cannot help.
			r.Ls("latest").MustNotContainEntry("deleted-later.txt")

			// find searches every snapshot by default and still finds it.
			r.FindJSON("deleted-later.txt").
				MustHaveSearchedSnapshots(2).
				MustHaveVersions("docs/deleted-later.txt", 1).
				MustHaveVersionInSnapshots("docs/deleted-later.txt", 0, 1)

			// Take the restore command out of the rendered output and run it.
			rendered := r.Find("deleted-later.txt").
				MustContain("docs/deleted-later.txt").
				MustContain("Restore the newest version").
				Raw()

			outDir := filepath.Join(t.TempDir(), "recovered")
			args := restoreArgsFromHint(t, rendered, outDir)
			h.Run(append(args, r.authArgs...)...)

			got, err := os.ReadFile(filepath.Join(outDir, "docs", "deleted-later.txt"))
			if err != nil {
				t.Fatalf("the restore command find printed did not produce the file: %v", err)
			}
			if string(got) != secret {
				t.Errorf("recovered content = %q, want %q", got, secret)
			}
		},
	})
}

// restoreArgsFromHint parses the `cloudstic restore ...` line find prints and
// returns it as argv, with the output path redirected into the test's temp
// directory. Everything else is taken verbatim, so a wrong flag name or a
// missing argument fails the test rather than being papered over.
func restoreArgsFromHint(t *testing.T, output, outDir string) []string {
	t.Helper()

	for line := range strings.SplitSeq(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "cloudstic" || fields[1] != "restore" {
			continue
		}
		args := fields[1:] // drop the binary name; the harness supplies it
		for i := 0; i < len(args)-1; i++ {
			if args[i] == "-output" {
				args[i+1] = outDir
				return args
			}
		}
		t.Fatalf("the restore hint has no -output flag, so it cannot be run as printed: %q", line)
	}
	t.Fatalf("no restore hint found in find output:\n%s", output)
	return nil
}

// TestCLI_Feature_FindDeltaScanMatchesFullScan is the equivalence check the
// delta scan is gated on, run here against a real layered store — compression,
// encryption, and packfiles — rather than the in-memory store the engine unit
// tests use. A delta scan that is correct over a plain map but wrong once reads
// are batched into packs would pass those and fail here.
func TestCLI_Feature_FindDeltaScanMatchesFullScan(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "find_delta_equivalence",
		sourceFilter: localOnlySource,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			r := h.MustInitEncrypted()

			// Enough churn for the diff to have real work to do: files added,
			// edited, deleted, and left alone across several snapshots.
			r.WithFile("a/one.txt", "1").
				WithFile("a/two.txt", "2").
				WithFile("b/three.txt", "3").
				Backup()
			r.WithFile("a/one.txt", "1 edited").Backup()
			r.WithFile("c/four.txt", "4").Backup()
			r.RemoveFile("a/two.txt").Backup()
			r.WithFile("a/two.txt", "2 restored").Backup()

			delta := r.FindJSON("*.txt")
			full := r.FindJSON("*.txt", "-no-delta")

			if got, want := renderFindMatches(delta), renderFindMatches(full); got != want {
				t.Errorf("delta scan and -no-delta disagree over a real store\ndelta:\n%s\nfull:\n%s", got, want)
			}
			if delta.Result.SnapshotsSearched != 5 {
				t.Errorf("searched %d snapshots, want 5", delta.Result.SnapshotsSearched)
			}

			// The delta scan is still correct if it silently degrades to a walk
			// per snapshot, so nothing above would notice. This is what does.
			if delta.Result.EntriesScanned >= full.Result.EntriesScanned {
				t.Errorf("delta scan visited %d entries against the full scan's %d: the structural sharing is not being exploited",
					delta.Result.EntriesScanned, full.Result.EntriesScanned)
			}
		},
	})
}

// renderFindMatches flattens a result into a comparable form. Counters and
// timings are deliberately excluded: the two scanners are meant to agree on
// what they found, not on what it cost.
func renderFindMatches(r *findResult) string {
	var b strings.Builder
	for _, m := range r.Result.Matches {
		fmt.Fprintf(&b, "match %s %s\n", m.FileID, m.ContentHash)
		for _, v := range m.Versions {
			fmt.Fprintf(&b, "  %s %s %s", v.Ref, v.Name, strings.Join(v.Paths, "|"))
			for _, s := range v.Snapshots {
				fmt.Fprintf(&b, " %s", s.Ref)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// TestCLI_Feature_FindByContent covers the axis the default grouping
// deliberately does not collapse: two distinct files holding identical bytes.
func TestCLI_Feature_FindByContent(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "find_by_content",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			const shared = "byte-for-byte identical"
			r := h.
				WithFile("docs/report.pdf", shared).
				WithFile("backup/report.pdf", shared).
				WithFile("docs/other.pdf", "different").
				MustInitEncrypted()
			r.Backup()

			// Separate by default: restoring one is not restoring the other.
			r.FindJSON("report.pdf").MustMatchPaths("backup/report.pdf", "docs/report.pdf")

			// Grouped on request, which is how duplicates are found.
			grouped := r.FindJSON("report.pdf", "-by-content")
			if len(grouped.Result.Matches) != 1 {
				t.Fatalf("-by-content should report one content group, got %d: %v",
					len(grouped.Result.Matches), grouped.matchedPaths())
			}
			if got := len(grouped.Result.Matches[0].Versions); got != 2 {
				t.Errorf("the content group should hold both files, got %d versions", got)
			}
			if grouped.Result.GroupedBy != "content" {
				t.Errorf("GroupedBy = %q, want content", grouped.Result.GroupedBy)
			}
		},
	})
}

// TestCLI_Feature_FindPredicates checks the predicate surface against a real
// directory tree, where paths are reconstructed from the parent chain rather
// than handed to the matcher.
func TestCLI_Feature_FindPredicates(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "find_predicates",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			r := h.
				WithFile("docs/report.pdf", "small").
				WithFile("docs/2026/report.pdf", strings.Repeat("x", 4096)).
				WithFile("docs/2026/notes.txt", "notes").
				WithFile("archive/report.pdf", "archived").
				MustInitEncrypted()
			r.Backup()

			t.Run("basename glob spans directories", func(t *testing.T) {
				r.FindJSON("*.pdf").MustMatchPaths(
					"archive/report.pdf", "docs/2026/report.pdf", "docs/report.pdf")
			})

			t.Run("path glob does not", func(t *testing.T) {
				r.FindJSON("docs/*.pdf").MustMatchPaths("docs/report.pdf")
			})

			t.Run("double star spans any depth", func(t *testing.T) {
				r.FindJSON("docs/**/report.pdf").MustMatchPaths(
					"docs/2026/report.pdf", "docs/report.pdf")
			})

			t.Run("regex matches the full path", func(t *testing.T) {
				r.FindJSON("-regex", `2026/.*\.pdf$`).MustMatchPaths("docs/2026/report.pdf")
			})

			t.Run("case insensitive", func(t *testing.T) {
				r.FindJSON("-name", "REPORT.PDF", "-i").MustMatchPaths(
					"archive/report.pdf", "docs/2026/report.pdf", "docs/report.pdf")
			})

			t.Run("size", func(t *testing.T) {
				r.FindJSON("-name", "*", "-size", "+1k").MustMatchPaths("docs/2026/report.pdf")
			})

			t.Run("type folder", func(t *testing.T) {
				r.FindJSON("-type", "d").MustMatchPaths("archive", "docs", "docs/2026")
			})

			t.Run("type file excludes folders", func(t *testing.T) {
				r.FindJSON("-name", "docs", "-type", "f").MustMatchPaths()
			})
		},
	})
}

// TestCLI_Feature_FindSnapshotSelectors checks that scoping narrows the search
// rather than merely filtering its output.
func TestCLI_Feature_FindSnapshotSelectors(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "find_snapshot_selectors",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			r := h.WithFile("docs/notes.txt", "first").MustInitEncrypted()
			r.Backup()
			first := r.List().FirstSnapshotID()

			r.WithFile("docs/notes.txt", "second, longer").Backup()
			r.WithFile("docs/notes.txt", "third, longer still").Backup()

			// A snapshot id resolves, and only that snapshot is scanned.
			scoped := r.FindJSON("notes.txt", "-snapshot", first).MustHaveSearchedSnapshots(1)
			scoped.MustHaveVersions("docs/notes.txt", 1)

			// A short prefix resolves to the same snapshot.
			r.FindJSON("notes.txt", "-snapshot", first[:8]).MustHaveSearchedSnapshots(1)

			// "latest" resolves, and picks the newest version.
			latest := r.FindJSON("notes.txt", "-snapshot", "latest").MustHaveSearchedSnapshots(1)
			if got := latest.MustMatchPath("docs/notes.txt").Versions[0].Size; got != int64(len("third, longer still")) {
				t.Errorf("latest version size = %d, want the newest content", got)
			}

			// -latest N takes the newest N.
			r.FindJSON("notes.txt", "-latest", "2").MustHaveSearchedSnapshots(2)

			// An unknown snapshot is an error, not an empty result — "not found"
			// and "found nothing" must not look the same.
			r.FindExpectFail("notes.txt", "-snapshot", "0123456789abcdef").
				MustContainAnyFold("not found")
		},
	})
}

// TestCLI_Feature_FindExitStatus pins the two cases scripts depend on: an
// empty result is a successful search, and a query with no predicate is refused
// rather than dumping the whole repository.
func TestCLI_Feature_FindExitStatus(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "find_exit_status",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			r := h.WithFile("docs/notes.txt", "content").MustInitEncrypted()
			r.Backup()

			// Exit 0 with no matches, consistent with find(1) and with `list`
			// on an empty repository.
			r.Find("no-such-file.dat").MustContain("No matches")

			empty := r.FindJSON("no-such-file.dat")
			if len(empty.Result.Matches) != 0 {
				t.Errorf("expected no matches, got %v", empty.matchedPaths())
			}

			r.FindExpectFail().MustContainAnyFold("at least one predicate")
		},
	})
}

// TestCLI_Feature_FindKeepsJSONClean guards the contract a scripted caller
// depends on: stdout under -json is the result and nothing else, even when the
// search has something to say about how it ran.
func TestCLI_Feature_FindKeepsJSONClean(t *testing.T) {
	runFeatureMatrix(t, featureSpec{
		name:         "find_json_is_clean",
		sourceFilter: localOnlySource,
		storeFilter:  localOnlyStore,
		test: func(t *testing.T, h *harness, entry matrixEntry) {
			r := h.WithFile("docs/notes.txt", "content").MustInitEncrypted()
			r.Backup()

			// -regex has no cheap basename prefilter, so find warns. The warning
			// must reach stderr without landing in the JSON stream.
			args := append([]string{"find", "-json", "-regex", `notes\.txt$`}, r.authArgs...)
			stdout := runStdoutOnly(t, h.bin, args...)

			var decoded struct {
				Matches []struct {
					FileID string `json:"file_id"`
				} `json:"matches"`
				Warnings []string `json:"warnings"`
			}
			newCommandResult(t, stdout).MustUnmarshalJSON(&decoded)

			if len(decoded.Matches) != 1 {
				t.Errorf("decoded %d matches, want 1", len(decoded.Matches))
			}
			if len(decoded.Warnings) == 0 {
				t.Error("the JSON payload should carry the prefilter warning as a field")
			}
			if strings.Contains(stdout, "warning: ") {
				t.Errorf("the rendered warning line contaminated stdout:\n%s", stdout)
			}
		},
	})
}
