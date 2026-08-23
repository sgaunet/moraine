package cli_test

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sgaunet/moraine/internal/cli"
)

// sortedFixture runs a real `sort` and returns the source and destination roots, so
// the undo tests act on a manifest moraine actually wrote.
func sortedFixture(t *testing.T) (src, dest string) {
	t.Helper()
	src, dest = t.TempDir(), t.TempDir()
	writePNG(t, filepath.Join(src, "a.png"))
	writeCLIFile(t, filepath.Join(src, "a.png.xmp"), "sidecar")

	code := cli.Execute("dev", []string{
		"sort", "--sample", "0", "--exiftool", stubExif(t), "--dest", dest, src,
	}, io.Discard, io.Discard)
	if code != 0 {
		t.Fatalf("sort exit = %d, want 0", code)
	}
	return src, dest
}

// listFiles returns every regular file under root, relative to it, sorted.
func listFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestUndoDeleteGivesBackTheDestination is the end-to-end promise of the feature:
// after `sort` then `undo --delete`, the destination holds none of the copies and
// the source is exactly as it was.
func TestUndoDeleteGivesBackTheDestination(t *testing.T) {
	src, dest := sortedFixture(t)
	sourceBefore := listFiles(t, src)
	if len(listFiles(t, dest)) < 3 { // photo + companion + manifest
		t.Fatalf("sort placed too little to test an undo: %v", listFiles(t, dest))
	}

	var out strings.Builder
	code := cli.Execute("dev", []string{"undo", "--delete", dest}, &out, io.Discard)
	if code != 0 {
		t.Fatalf("undo exit = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "removed=2") {
		t.Errorf("summary should account for the photo and its companion; got %q", out.String())
	}

	for _, rel := range listFiles(t, dest) {
		if !strings.HasPrefix(rel, ".moraine") {
			t.Errorf("%s should have been removed from the destination", rel)
		}
	}
	after := listFiles(t, src)
	if strings.Join(after, "|") != strings.Join(sourceBefore, "|") {
		t.Errorf("undo touched the source: before %v, after %v", sourceBefore, after)
	}
}

func TestUndoIsDryRunByDefault(t *testing.T) {
	_, dest := sortedFixture(t)
	before := listFiles(t, dest)

	var out strings.Builder
	code := cli.Execute("dev", []string{"undo", dest}, &out, io.Discard)
	if code != 0 {
		t.Fatalf("undo exit = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "would_remove=2") || !strings.Contains(out.String(), "removed=0") {
		t.Errorf("a dry run must report a plan and remove nothing; got %q", out.String())
	}
	if strings.Join(listFiles(t, dest), "|") != strings.Join(before, "|") {
		t.Error("a dry run must leave the destination untouched")
	}
}

// TestUndoTwiceStepsBackARun: the second undo must not re-report the run it just
// finished; with only one run recorded there is nothing left to undo.
func TestUndoTwiceStepsBackARun(t *testing.T) {
	_, dest := sortedFixture(t)
	if code := cli.Execute("dev", []string{"undo", "--delete", dest}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("first undo exit = %d, want 0", code)
	}
	var errb strings.Builder
	code := cli.Execute("dev", []string{"undo", "--delete", dest}, io.Discard, &errb)
	if code != 1 {
		t.Fatalf("second undo exit = %d, want 1 (nothing left to undo)", code)
	}
	if !strings.Contains(errb.String(), "no run to undo") {
		t.Errorf("stderr should explain there is nothing to undo; got %q", errb.String())
	}
}

func TestUndoWithoutAManifestIsARuntimeError(t *testing.T) {
	var errb strings.Builder
	if code := cli.Execute("dev", []string{"undo", t.TempDir()}, io.Discard, &errb); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errb.String(), "error:") {
		t.Errorf("stderr should carry the runtime error; got %q", errb.String())
	}
}

func TestUndoMissingDestinationIsARuntimeError(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "nope")
	if code := cli.Execute("dev", []string{"undo", dest}, io.Discard, io.Discard); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
}

func TestUndoWrongArgCountIsAUsageError(t *testing.T) {
	for _, args := range [][]string{{"undo"}, {"undo", t.TempDir(), t.TempDir()}} {
		if code := cli.Execute("dev", args, io.Discard, io.Discard); code != 2 {
			t.Errorf("%v: exit = %d, want 2", args, code)
		}
	}
}

// TestSortIncrementalReRunPlacesNothing covers the flag at the transport level: a
// second incremental pass over an unchanged library is a no-op.
func TestSortIncrementalReRunPlacesNothing(t *testing.T) {
	src, dest := sortedFixture(t)

	var out strings.Builder
	code := cli.Execute("dev", []string{
		"sort", "--incremental", "--sample", "0", "--exiftool", stubExif(t), "--dest", dest, src,
	}, &out, io.Discard)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "copied=0") || !strings.Contains(out.String(), "skipped=1") {
		t.Errorf("incremental re-run should place nothing; got %q", out.String())
	}
	if !strings.Contains(out.String(), "companions_skipped=1") {
		t.Errorf("the companion should be skipped too; got %q", out.String())
	}
}
