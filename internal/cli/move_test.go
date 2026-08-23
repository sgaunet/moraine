package cli_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sgaunet/moraine/internal/cli"
)

// sourcesOf lists the regular files left in a source directory.
func sourcesOf(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if e.Type().IsRegular() {
			out = append(out, e.Name())
		}
	}
	return out
}

// moveFixture builds a source holding one photo plus one companion and returns the
// arguments that move them into a fresh destination, plus both directories.
func moveFixture(t *testing.T, extra ...string) (args []string, src, dest string) {
	t.Helper()
	src, dest = t.TempDir(), t.TempDir()
	writePNG(t, filepath.Join(src, "a.png"))
	writeCLIFile(t, filepath.Join(src, "a.xmp"), "base")
	args = append([]string{
		"sort", "--sample", "0", "--exiftool", stubExif(t), "--move", "--dest", dest,
	}, extra...)
	return append(args, src), src, dest
}

func TestSortMoveReportsAndRemovesSources(t *testing.T) {
	args, src, _ := moveFixture(t)
	var out bytes.Buffer
	if code := cli.Execute("dev", args, &out, io.Discard); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	// Both the photo and its companion were moved.
	if !strings.Contains(out.String(), "moved=2") {
		t.Errorf("summary must report moved=2; got: %s", out.String())
	}
	if left := sourcesOf(t, src); len(left) != 0 {
		t.Errorf("the sources must be gone after a verified move, still there: %v", left)
	}
}

func TestSortMoveJSONMarksEachRecord(t *testing.T) {
	args, _, _ := moveFixture(t, "--output=json")
	var out bytes.Buffer
	if code := cli.Execute("dev", args, &out, io.Discard); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var doc struct {
		Results []struct {
			Action string `json:"action"`
			Moved  bool   `json:"moved"`
		} `json:"results"`
		Summary struct {
			Copied int `json:"copied"`
			Moved  int `json:"moved"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, out.String())
	}
	if doc.Summary.Moved != 2 || doc.Summary.Copied != 1 {
		t.Errorf("summary = %+v; want Moved=2 Copied=1", doc.Summary)
	}
	for _, r := range doc.Results {
		if !r.Moved {
			t.Errorf("every placed record of a move run must be marked: %+v", r)
		}
	}
}

// --move --dry-run is the preview Principle V asks a destructive action to have: it
// must write nothing and delete nothing.
func TestSortMoveDryRunTouchesNothing(t *testing.T) {
	args, src, dest := moveFixture(t, "--dry-run")
	var out bytes.Buffer
	if code := cli.Execute("dev", args, &out, io.Discard); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "moved=0") {
		t.Errorf("a dry run must report moved=0; got: %s", out.String())
	}
	if left := sourcesOf(t, src); len(left) != 2 {
		t.Errorf("a dry run must keep every source, found %v", left)
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a dry run must write nothing, found %d entries in the destination", len(entries))
	}
}

// A second move over an already-sorted library skips everything, and a skip never
// removes an original — so the sources stay put and the user is told nothing moved.
func TestSortMoveSecondRunKeepsSourcesItOnlyRecognises(t *testing.T) {
	src, dest := t.TempDir(), t.TempDir()
	writePNG(t, filepath.Join(src, "a.png"))
	exif := stubExif(t)
	copyArgs := []string{"sort", "--sample", "0", "--exiftool", exif, "--dest", dest, src}
	if code := cli.Execute("dev", copyArgs, io.Discard, io.Discard); code != 0 {
		t.Fatalf("first (copy) run: exit = %d", code)
	}

	moveArgs := []string{"sort", "--sample", "0", "--exiftool", exif, "--move", "--dest", dest, src}
	var out bytes.Buffer
	if code := cli.Execute("dev", moveArgs, &out, io.Discard); code != 0 {
		t.Fatalf("move run: exit = %d", code)
	}
	if !strings.Contains(out.String(), "moved=0") || !strings.Contains(out.String(), "skipped=1") {
		t.Errorf("want skipped=1 moved=0; got: %s", out.String())
	}
	if left := sourcesOf(t, src); len(left) != 1 {
		t.Errorf("a skipped photo's source must survive --move, found %v", left)
	}
}

// undo after a move run refuses to remove the copies, because the originals are gone.
func TestUndoAfterAMoveRunKeepsTheCopies(t *testing.T) {
	args, _, dest := moveFixture(t)
	if code := cli.Execute("dev", args, io.Discard, io.Discard); code != 0 {
		t.Fatalf("move run: exit = %d", code)
	}

	var out bytes.Buffer
	if code := cli.Execute("dev", []string{"undo", "--delete", dest}, &out, io.Discard); code != 0 {
		t.Fatalf("undo: exit = %d", code)
	}
	if !strings.Contains(out.String(), "removed=0") || !strings.Contains(out.String(), "kept=2") {
		t.Errorf("undo must keep both moved files; got: %s", out.String())
	}
}
