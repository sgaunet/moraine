package scan_test

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/sgaunet/moraine/internal/photo"
	"github.com/sgaunet/moraine/internal/scan"
)

// discard is a logger for the tests that do not assert on log output.
func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

func write(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestScanRecursiveAndFilters(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "a.jpg"))
	write(t, filepath.Join(src, "b.JPEG"))
	write(t, filepath.Join(src, "sub", "c.png"))
	write(t, filepath.Join(src, "sub", "deep", "d.HEIC"))
	write(t, filepath.Join(src, "e.heif"))
	write(t, filepath.Join(src, "raw.cr2"))         // RAW now supported (feature 003)
	write(t, filepath.Join(src, "sub", "shot.dng")) // RAW now supported
	write(t, filepath.Join(src, "notes.txt"))       // ignored
	write(t, filepath.Join(src, "movie.mp4"))       // ignored

	dest := filepath.Join(src, "_sorted")

	found, err := scan.Scan(src, dest, discard())
	if err != nil {
		t.Fatal(err)
	}
	names := relNames(t, src, found)
	want := []string{"a.jpg", "b.JPEG", "e.heif", "raw.cr2", "sub/c.png", "sub/deep/d.HEIC", "sub/shot.dng"}
	if !equal(names, want) {
		t.Fatalf("found %v; want %v", names, want)
	}
}

func TestScanExcludesDestRootUnderSource(t *testing.T) {
	src := t.TempDir()
	dest := filepath.Join(src, "_sorted") // destination nested under source

	write(t, filepath.Join(src, "keep.jpg"))
	// Already-sorted photos living under _sorted must be ignored.
	write(t, filepath.Join(dest, "trip", "old1.jpg"))
	write(t, filepath.Join(dest, "old2.png"))

	found, err := scan.Scan(src, dest, discard())
	if err != nil {
		t.Fatal(err)
	}
	names := relNames(t, src, found)
	if !equal(names, []string{"keep.jpg"}) {
		t.Fatalf("found %v; want only keep.jpg (destRoot excluded)", names)
	}
}

func TestScanFormatsClassified(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "p.heic"))
	found, err := scan.Scan(src, filepath.Join(src, "_sorted"), discard())
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Format != photo.HEIC {
		t.Fatalf("found %+v; want one HEIC", found)
	}
}

func TestScanSkipsUnreadableSubdirWithoutAborting(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits do not deny this user")
	}
	src := t.TempDir()
	write(t, filepath.Join(src, "before.jpg"))
	write(t, filepath.Join(src, "sub", "after.jpg"))
	locked := filepath.Join(src, "locked")
	write(t, filepath.Join(locked, "hidden.jpg"))
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) }) // let t.TempDir clean up

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))

	found, err := scan.Scan(src, filepath.Join(src, "_sorted"), logger)
	if err != nil {
		t.Fatalf("one unreadable directory must not abort the scan: %v", err)
	}
	// The readable photos on both sides of the unreadable directory survive.
	if names := relNames(t, src, found); !equal(names, []string{"before.jpg", "sub/after.jpg"}) {
		t.Fatalf("found %v; want the readable photos only", names)
	}
	if !strings.Contains(logs.String(), "path skipped") {
		t.Errorf("the skip must be logged; got:\n%s", logs.String())
	}
}

func TestScanUnreadableSourceRootIsFatal(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits do not deny this user")
	}
	src := filepath.Join(t.TempDir(), "root")
	write(t, filepath.Join(src, "a.jpg"))
	if err := os.Chmod(src, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(src, 0o755) })

	if _, err := scan.Scan(src, filepath.Join(src, "_sorted"), discard()); err == nil {
		t.Fatal("an unreadable source root must still be an error")
	}
}

func relNames(t *testing.T, root string, found []scan.Found) []string {
	t.Helper()
	out := make([]string, 0, len(found))
	for _, f := range found {
		rel, err := filepath.Rel(root, f.Path)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, filepath.ToSlash(rel))
	}
	sort.Strings(out)
	return out
}

func equal(a, b []string) bool {
	sort.Strings(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestScanExcludesDestReachedThroughSymlink pins identity-based exclusion. Cleaned
// strings are not identity: on macOS /tmp is a symlink to /private/tmp, so a
// destination named through one and walked through the other would not match, and
// the previous run's copies would be re-ingested as new photos.
func TestScanExcludesDestReachedThroughSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need elevation on windows")
	}
	src := t.TempDir()
	realDest := filepath.Join(src, "real_sorted")
	write(t, filepath.Join(src, "keep.jpg"))
	write(t, filepath.Join(realDest, "trip", "already-sorted.jpg"))

	// The destination is named through a symlink; the walk meets the real directory.
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(realDest, link); err != nil {
		t.Fatal(err)
	}

	found, err := scan.Scan(src, link, discard())
	if err != nil {
		t.Fatal(err)
	}
	if names := relNames(t, src, found); !equal(names, []string{"keep.jpg"}) {
		t.Fatalf("found %v; want only keep.jpg (the destination is the same directory)", names)
	}
}

// TestScanSymlinkHandling pins the documented, asymmetric rule: a symlinked
// directory is never descended into (and says so at debug level), while a symlinked
// file with a recognised extension is listed like any other photo.
func TestScanSymlinkHandling(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need elevation on windows")
	}
	outside := t.TempDir()
	write(t, filepath.Join(outside, "hidden.jpg"))
	target := filepath.Join(outside, "target.jpg")
	write(t, target)

	src := t.TempDir()
	write(t, filepath.Join(src, "plain.jpg"))
	if err := os.Symlink(outside, filepath.Join(src, "linked-dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(src, "linked.jpg")); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	found, err := scan.Scan(src, filepath.Join(src, "_sorted"), logger)
	if err != nil {
		t.Fatal(err)
	}
	// linked.jpg is in; hidden.jpg behind the symlinked directory is not.
	if names := relNames(t, src, found); !equal(names, []string{"linked.jpg", "plain.jpg"}) {
		t.Fatalf("found %v; want the plain photo and the symlinked file only", names)
	}
	if !strings.Contains(logs.String(), "symlink not followed") {
		t.Errorf("the skipped directory symlink must be logged; got:\n%s", logs.String())
	}
}
