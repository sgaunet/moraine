package organize_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/organize"
	"github.com/sgaunet/moraine/internal/photo"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSafeJoin(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name    string
		subdir  string
		wantErr bool
	}{
		{"simple", filepath.Join("mountain", "2025", "2025-08-12"), false},
		{"absolute rejected", string(filepath.Separator) + "etc", true},
		{"escape rejected", filepath.Join("..", "..", "evil"), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := organize.SafeJoin(root, tc.subdir)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				if !errors.Is(err, organize.ErrInvalidDestSubdir) {
					t.Fatalf("expected ErrInvalidDestSubdir, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestUniqueName(t *testing.T) {
	dir := t.TempDir()
	if got := organize.UniqueName(dir, "a.jpg"); got != "a.jpg" {
		t.Fatalf("no collision: want a.jpg, got %q", got)
	}
	writeFile(t, filepath.Join(dir, "a.jpg"), "x")
	if got := organize.UniqueName(dir, "a.jpg"); got != "a (1).jpg" {
		t.Fatalf("first collision: want 'a (1).jpg', got %q", got)
	}
	writeFile(t, filepath.Join(dir, "a (1).jpg"), "x")
	if got := organize.UniqueName(dir, "a.jpg"); got != "a (2).jpg" {
		t.Fatalf("second collision: want 'a (2).jpg', got %q", got)
	}
}

// tmpLeftovers lists the in-progress ".moraine-*.tmp" files left in dir. Every
// copy must clean up after itself, so this is expected to be empty once a copy has
// returned — whether it succeeded or failed.
func tmpLeftovers(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".moraine-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")
	writeFile(t, src, "hello")

	if err := organize.CopyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "hello" {
		t.Fatalf("content: want hello, got %q", got)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("source must be preserved: %v", err)
	}
	if left := tmpLeftovers(t, dir); len(left) != 0 {
		t.Fatalf("a successful copy left temporary files behind: %v", left)
	}

	// Refuse to overwrite an existing destination — and leave it untouched.
	err := organize.CopyFile(src, dst)
	if err == nil {
		t.Fatal("expected error overwriting existing dst")
	}
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("want an fs.ErrExist error, got %v", err)
	}
	if again, _ := os.ReadFile(dst); string(again) != "hello" {
		t.Fatalf("existing destination was modified: %q", again)
	}
	if left := tmpLeftovers(t, dir); len(left) != 0 {
		t.Fatalf("a refused copy left temporary files behind: %v", left)
	}
}

// TestCopyFilePreservesModTime pins the mtime carry-over. It is load-bearing, not
// cosmetic: exifmeta falls back to the file's modification time when a photo has no
// readable EXIF date, so a copy stamped "now" would file such a photo under the day
// it was copied instead of the day it was taken.
func TestCopyFilePreservesModTime(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")
	writeFile(t, src, "hello")

	want := time.Date(2021, 8, 12, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(src, want, want); err != nil {
		t.Fatal(err)
	}
	if err := organize.CopyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.ModTime(); !got.Equal(want) {
		t.Fatalf("destination mtime = %s, want the source's %s", got, want)
	}
}

// TestCopyFileFailureLeavesNothingBehind is the regression test for the durability
// defect this replaced: a copy used to write straight to the destination, so an
// interrupted one left a truncated file sitting on the canonical name. Every later run then read that
// stub as "different content" and suffix-renamed the real photo, letting the stub
// keep the good name forever. A failed copy must now leave the destination
// directory exactly as it found it.
func TestCopyFileFailureLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "dest")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	// A directory opens fine but fails to read, which fails the copy mid-flight.
	src := filepath.Join(dir, "src-is-a-dir")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dest, "IMG_1.jpg")

	if err := organize.CopyFile(src, dst); err == nil {
		t.Fatal("expected the copy to fail")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("a failed copy must not create the destination (stat err = %v)", err)
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("a failed copy left files behind: %v", names)
	}
}

func TestSameContent(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	c := filepath.Join(dir, "c")
	writeFile(t, a, "same")
	writeFile(t, b, "same")
	writeFile(t, c, "different-length")

	if ok, err := organize.SameContent(a, b); err != nil || !ok {
		t.Fatalf("identical: ok=%v err=%v", ok, err)
	}
	if ok, err := organize.SameContent(a, c); err != nil || ok {
		t.Fatalf("size mismatch must be false: ok=%v err=%v", ok, err)
	}
	// same size, different bytes
	d := filepath.Join(dir, "d")
	writeFile(t, d, "samz")
	writeFile(t, a, "same")
	if ok, err := organize.SameContent(a, d); err != nil || ok {
		t.Fatalf("different bytes must be false: ok=%v err=%v", ok, err)
	}
}

func clusterOf(t *testing.T, dir string, names ...string) photo.Cluster {
	t.Helper()
	date := time.Date(2025, 8, 12, 10, 0, 0, 0, time.UTC)
	ps := make([]photo.Photo, 0, len(names))
	for i, n := range names {
		p := filepath.Join(dir, n)
		writeFile(t, p, "content-"+n)
		ps = append(ps, photo.Photo{Path: p, Name: n, Taken: date.Add(time.Duration(i) * time.Minute), Format: photo.JPEG})
	}
	return photo.Cluster{Photos: ps, Start: date, End: date}
}

func TestPlaceCopiesIntoLayout(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	c := clusterOf(t, src, "IMG_1.jpg", "IMG_2.jpg")

	results := organize.New(dest).Place(context.Background(), c, "nature")
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d", len(results))
	}
	wantDir := filepath.Join(dest, "nature", "2025", "2025-08-12")
	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("placement error: %v", r.Err)
		}
		if r.Action != organize.ActionCopied {
			t.Fatalf("want copied, got %s", r.Action)
		}
		if filepath.Dir(r.Dest) != wantDir {
			t.Fatalf("want dir %q, got %q", wantDir, filepath.Dir(r.Dest))
		}
		if _, err := os.Stat(r.Dest); err != nil {
			t.Fatalf("dest missing: %v", err)
		}
		if _, err := os.Stat(r.Source); err != nil {
			t.Fatalf("source must be preserved: %v", err)
		}
	}
}

func TestPlaceSkipsIdenticalAndRenamesDifferent(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	c := clusterOf(t, src, "IMG_1.jpg")

	org := organize.New(dest)
	first := org.Place(context.Background(), c, "family")
	if first[0].Action != organize.ActionCopied {
		t.Fatalf("first run: want copied, got %s", first[0].Action)
	}

	// Re-run identical → skipped.
	second := org.Place(context.Background(), c, "family")
	if second[0].Action != organize.ActionSkippedIdentical {
		t.Fatalf("re-run: want skipped-identical, got %s", second[0].Action)
	}

	// Different content, same name → renamed.
	writeFile(t, filepath.Join(src, "IMG_1.jpg"), "totally-different-bytes")
	third := org.Place(context.Background(), c, "family")
	if third[0].Action != organize.ActionRenamed {
		t.Fatalf("different content: want renamed, got %s", third[0].Action)
	}
	if filepath.Base(third[0].Dest) != "IMG_1 (1).jpg" {
		t.Fatalf("want suffixed name, got %q", filepath.Base(third[0].Dest))
	}
}

// TestPlaceIsIdempotentAfterCollisionRename is the regression test for the
// duplication bug: dedup used to compare content only against the exact target
// name, so once a photo had been placed as "IMG_1 (1).jpg" every later run
// re-collided on "IMG_1.jpg", saw (1) taken, and copied the same bytes again as
// (2), (3), … Re-runs must be no-ops instead.
func TestPlaceIsIdempotentAfterCollisionRename(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	c := clusterOf(t, src, "IMG_1.jpg")
	org := organize.New(dest)
	dir := filepath.Join(dest, "family", "2025", "2025-08-12")

	// Occupy the base name with different content, so the photo lands on " (1)".
	org.Place(context.Background(), c, "family")
	writeFile(t, filepath.Join(src, "IMG_1.jpg"), "second-generation-bytes")
	renamed := org.Place(context.Background(), c, "family")
	if renamed[0].Action != organize.ActionRenamed {
		t.Fatalf("setup: want renamed, got %s", renamed[0].Action)
	}
	if filepath.Base(renamed[0].Dest) != "IMG_1 (1).jpg" {
		t.Fatalf("setup: want 'IMG_1 (1).jpg', got %q", filepath.Base(renamed[0].Dest))
	}

	// Every further run must recognise the content it already placed under the
	// suffixed name and write nothing.
	for run := 3; run <= 5; run++ {
		again := org.Place(context.Background(), c, "family")
		if again[0].Err != nil {
			t.Fatalf("run %d: %v", run, again[0].Err)
		}
		if again[0].Action != organize.ActionSkippedIdentical {
			t.Fatalf("run %d: want skipped-identical, got %s", run, again[0].Action)
		}
		if got := filepath.Base(again[0].Dest); got != "IMG_1 (1).jpg" {
			t.Fatalf("run %d: Dest = %q; want the already-placed 'IMG_1 (1).jpg'", run, got)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "IMG_1 (2).jpg")); err == nil {
		t.Fatal("re-runs duplicated the photo as 'IMG_1 (2).jpg'")
	}

	// A third distinct content still gets the next free suffix.
	writeFile(t, filepath.Join(src, "IMG_1.jpg"), "third-generation-bytes")
	third := org.Place(context.Background(), c, "family")
	if third[0].Action != organize.ActionRenamed {
		t.Fatalf("new content: want renamed, got %s", third[0].Action)
	}
	if got := filepath.Base(third[0].Dest); got != "IMG_1 (2).jpg" {
		t.Fatalf("new content: want 'IMG_1 (2).jpg', got %q", got)
	}

	// …and is then itself deduplicated, including past an earlier variant.
	fourth := org.Place(context.Background(), c, "family")
	if fourth[0].Action != organize.ActionSkippedIdentical {
		t.Fatalf("re-run of the third content: want skipped-identical, got %s", fourth[0].Action)
	}
	if got := filepath.Base(fourth[0].Dest); got != "IMG_1 (2).jpg" {
		t.Fatalf("re-run of the third content: Dest = %q; want 'IMG_1 (2).jpg'", got)
	}

	// Exactly three files: the original and two generations, no duplicates.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("want 3 placed files, got %d: %v", len(entries), names)
	}
}

func TestPlaceCancelledContext(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	c := clusterOf(t, src, "IMG_1.jpg")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results := organize.New(dest).Place(ctx, c, "nature")
	if results[0].Err == nil {
		t.Fatal("expected context error")
	}
}
