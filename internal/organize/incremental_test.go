package organize_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/organize"
)

// fingerprint returns the size and modification time a manifest would have
// recorded for a placed file.
func fingerprint(t *testing.T, path string) (int64, time.Time) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size(), info.ModTime()
}

// placedOrg builds an Organizer whose Placed hook answers from a fixed table,
// the way an incremental run answers from the manifest index.
func placedOrg(dest string, table map[string]organize.Placement) *organize.Organizer {
	org := organize.New(dest)
	org.Placed = func(src string) (organize.Placement, bool) {
		p, ok := table[src]
		return p, ok
	}
	return org
}

// TestPlacedSkipsWithoutComparingContent is the point of the incremental path: a
// source whose recorded fingerprint still matches is skipped on the strength of
// the manifest alone. The destination here holds *different* bytes of the same
// size, so a run that compared content would have suffix-renamed instead — which
// is exactly what proves no comparison happened.
func TestPlacedSkipsWithoutComparingContent(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	c := clusterOf(t, src, "IMG_1.jpg")
	photoPath := c.Photos[0].Path

	placedDest := filepath.Join(dest, "family", "2025", "2025-08-12", "IMG_1.jpg")
	if err := os.MkdirAll(filepath.Dir(placedDest), 0o755); err != nil {
		t.Fatal(err)
	}
	size, mtime := fingerprint(t, photoPath)
	writeFile(t, placedDest, "xxxxxxxxxxxxxxxxxxxx"[:size]) // same size, different bytes
	if err := os.Chtimes(placedDest, time.Time{}, mtime); err != nil {
		t.Fatal(err)
	}

	org := placedOrg(dest, map[string]organize.Placement{
		photoPath: {Dest: placedDest, Size: size, ModTime: mtime},
	})
	results := org.Place(context.Background(), c, "family")
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if results[0].Action != organize.ActionSkippedIdentical {
		t.Fatalf("want skipped-identical from the manifest, got %s", results[0].Action)
	}
	if results[0].Dest != placedDest {
		t.Errorf("dest = %q, want the recorded %q", results[0].Dest, placedDest)
	}
}

func TestPlacedFallsThroughWhenFingerprintDiffers(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	c := clusterOf(t, src, "IMG_1.jpg")
	photoPath := c.Photos[0].Path
	size, mtime := fingerprint(t, photoPath)

	cases := map[string]organize.Placement{
		"source changed since the run": {Dest: filepath.Join(dest, "x.jpg"), Size: size + 1, ModTime: mtime},
		"copy no longer on disk":       {Dest: filepath.Join(dest, "gone.jpg"), Size: size, ModTime: mtime},
		"no destination recorded":      {Size: size, ModTime: mtime},
	}
	for name, placement := range cases {
		t.Run(name, func(t *testing.T) {
			runDest := t.TempDir()
			org := placedOrg(runDest, map[string]organize.Placement{photoPath: placement})
			results := org.Place(context.Background(), c, "family")
			if results[0].Err != nil {
				t.Fatalf("placement error: %v", results[0].Err)
			}
			if results[0].Action != organize.ActionCopied {
				t.Fatalf("want the normal copy, got %s", results[0].Action)
			}
			if _, err := os.Lstat(results[0].Dest); err != nil {
				t.Errorf("expected a real copy at %q: %v", results[0].Dest, err)
			}
		})
	}
}

func TestPlacedAppliesToCompanions(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	c := clusterOf(t, src, "IMG_1.jpg")
	photoPath := c.Photos[0].Path
	sidecar := photoPath + ".xmp"
	writeFile(t, sidecar, "<xmp/>")

	dir := filepath.Join(dest, "family", "2025", "2025-08-12")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	table := map[string]organize.Placement{}
	for _, s := range []string{photoPath, sidecar} {
		placed := filepath.Join(dir, filepath.Base(s))
		writeFile(t, placed, "placeholder")
		size, mtime := fingerprint(t, s)
		if err := os.Truncate(placed, size); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(placed, time.Time{}, mtime); err != nil {
			t.Fatal(err)
		}
		table[s] = organize.Placement{Dest: placed, Size: size, ModTime: mtime}
	}

	org := placedOrg(dest, table)
	org.Sidecars = true
	results := org.Place(context.Background(), c, "family")
	if len(results) != 2 {
		t.Fatalf("want the photo and its companion, got %d results", len(results))
	}
	for _, r := range results {
		if r.Action != organize.ActionSkippedIdentical {
			t.Errorf("%s: want skipped-identical, got %s", filepath.Base(r.Source), r.Action)
		}
	}
	if !results[1].IsCompanion || results[1].Of != photoPath {
		t.Errorf("second result should still be reported as a companion of the photo: %+v", results[1])
	}
}
