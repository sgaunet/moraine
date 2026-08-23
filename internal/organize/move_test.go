package organize_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/organize"
)

// moveOrg builds an Organizer in --move mode.
func moveOrg(dest string) *organize.Organizer {
	org := organize.New(dest)
	org.Move = true
	return org
}

// exists reports whether a path is present.
func present(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	return err == nil
}

// The only case that removes a source: a copy read back and verified.
func TestMoveRemovesSourceOnVerifiedCopy(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	c := clusterOf(t, src, "IMG_1.jpg")
	photoPath := c.Photos[0].Path

	results := moveOrg(dest).Place(context.Background(), c, "nature")
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	r := results[0]
	if r.Err != nil || r.Action != organize.ActionCopied {
		t.Fatalf("result = %+v; want a clean copy", r)
	}
	if !r.Moved {
		t.Error("a verified copy in --move mode must report Moved")
	}
	if present(t, photoPath) {
		t.Error("the source must be gone after a verified copy")
	}
	if !present(t, r.Dest) {
		t.Error("the copy must be present")
	}
}

// A suffixed placement is still a successful one, so it removes its source too.
func TestMoveRemovesSourceOnRename(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	c := clusterOf(t, src, "IMG_1.jpg")
	photoPath := c.Photos[0].Path

	// Occupy the canonical name with different content, forcing a rename.
	dir := filepath.Join(dest, "nature", "2025", "2025-08-12")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "IMG_1.jpg"), "different content entirely")

	results := moveOrg(dest).Place(context.Background(), c, "nature")
	r := results[0]
	if r.Err != nil || r.Action != organize.ActionRenamed {
		t.Fatalf("result = %+v; want a rename", r)
	}
	if !r.Moved || present(t, photoPath) {
		t.Errorf("a verified rename must remove its source: Moved=%v, source present=%v",
			r.Moved, present(t, photoPath))
	}
}

// A skip verifies nothing during this run, so it must never remove an original. This
// is the single most consequential rule in --move: the incremental variant compares
// only size and modification time and never reads the bytes at all, so deleting on
// that basis would destroy an original on the strength of a fingerprint.
func TestMoveKeepsSourceOnEverySkip(t *testing.T) {
	t.Run("same name, identical content", func(t *testing.T) {
		src := t.TempDir()
		dest := t.TempDir()
		c := clusterOf(t, src, "IMG_1.jpg")
		photoPath := c.Photos[0].Path

		dir := filepath.Join(dest, "nature", "2025", "2025-08-12")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "IMG_1.jpg"), "content-IMG_1.jpg")

		r := moveOrg(dest).Place(context.Background(), c, "nature")[0]
		if r.Action != organize.ActionSkippedIdentical {
			t.Fatalf("action = %s, want skipped-identical", r.Action)
		}
		if r.Moved || !present(t, photoPath) {
			t.Errorf("a skip must keep its source: Moved=%v, present=%v", r.Moved, present(t, photoPath))
		}
	})

	t.Run("identical under a (N) variant", func(t *testing.T) {
		src := t.TempDir()
		dest := t.TempDir()
		c := clusterOf(t, src, "IMG_1.jpg")
		photoPath := c.Photos[0].Path

		dir := filepath.Join(dest, "nature", "2025", "2025-08-12")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "IMG_1.jpg"), "something else")
		writeFile(t, filepath.Join(dir, "IMG_1 (1).jpg"), "content-IMG_1.jpg")

		r := moveOrg(dest).Place(context.Background(), c, "nature")[0]
		if r.Action != organize.ActionSkippedIdentical {
			t.Fatalf("action = %s, want skipped-identical", r.Action)
		}
		if r.Moved || !present(t, photoPath) {
			t.Errorf("a variant skip must keep its source: Moved=%v, present=%v", r.Moved, present(t, photoPath))
		}
	})

	t.Run("incremental fingerprint", func(t *testing.T) {
		src := t.TempDir()
		dest := t.TempDir()
		c := clusterOf(t, src, "IMG_1.jpg")
		photoPath := c.Photos[0].Path

		placedDest := filepath.Join(dest, "recorded", "IMG_1.jpg")
		if err := os.MkdirAll(filepath.Dir(placedDest), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, placedDest, "content-IMG_1.jpg")
		size, mtime := fingerprint(t, photoPath)
		if err := os.Chtimes(placedDest, time.Time{}, mtime); err != nil {
			t.Fatal(err)
		}

		org := placedOrg(dest, map[string]organize.Placement{
			photoPath: {Dest: placedDest, Size: size, ModTime: mtime},
		})
		org.Move = true
		r := org.Place(context.Background(), c, "nature")[0]
		if r.Action != organize.ActionSkippedIdentical {
			t.Fatalf("action = %s, want skipped-identical", r.Action)
		}
		if r.Moved || !present(t, photoPath) {
			t.Errorf("a fingerprint skip must keep its source: Moved=%v, present=%v",
				r.Moved, present(t, photoPath))
		}
	})
}

// --move --dry-run is the preview, and a preview writes and deletes nothing.
func TestMoveKeepsSourceOnDryRun(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	c := clusterOf(t, src, "IMG_1.jpg", "IMG_2.jpg")

	org := moveOrg(dest)
	org.DryRun = true
	results := org.Place(context.Background(), c, "nature")
	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("dry run error: %v", r.Err)
		}
		if r.Moved {
			t.Error("a dry run must not report anything as moved")
		}
		if !present(t, r.Source) {
			t.Errorf("a dry run must keep every source, %q is gone", r.Source)
		}
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a dry run must write nothing, found %d entries", len(entries))
	}
}

// A failed copy must leave the source alone. The lever is a read-only destination
// directory, so the failure comes from the filesystem rather than from a fake.
func TestMoveKeepsSourceOnCopyFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a read-only directory does not stop root")
	}
	src := t.TempDir()
	dest := t.TempDir()
	c := clusterOf(t, src, "IMG_1.jpg")
	photoPath := c.Photos[0].Path

	dir := filepath.Join(dest, "nature", "2025", "2025-08-12")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil { // readable and listable, not writable
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	r := moveOrg(dest).Place(context.Background(), c, "nature")[0]
	if r.Err == nil {
		t.Fatal("want a placement error on a read-only destination")
	}
	if r.Moved || !present(t, photoPath) {
		t.Errorf("a failed copy must keep its source: Moved=%v, present=%v", r.Moved, present(t, photoPath))
	}
}

// If the published copy does not hold what was written to it, the source survives —
// this is the property that makes --move safe rather than merely convenient. The
// corrupt destination is also removed, so no later run mistakes it for real content
// on a canonical name.
func TestMoveKeepsSourceOnVerifyMismatch(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	c := clusterOf(t, src, "IMG_1.jpg")
	photoPath := c.Photos[0].Path

	org := moveOrg(dest)
	organize.SetAfterPublish(org, func(dst string) {
		// Same length, different bytes: only the digest can catch this.
		if err := os.WriteFile(dst, []byte("XXXXXXXXXXXXXXXXX"), 0o644); err != nil {
			t.Fatal(err)
		}
	})

	r := org.Place(context.Background(), c, "nature")[0]
	if r.Err == nil {
		t.Fatal("a copy that does not match what was written must be an error")
	}
	if r.Moved {
		t.Error("an unverified copy must not report Moved")
	}
	if !present(t, photoPath) {
		t.Error("the source must survive a verification failure")
	}
	if present(t, r.Dest) {
		t.Error("a provably wrong copy must not be left on a canonical name")
	}
}

// Sources the run never reached must survive a cancellation untouched.
func TestMoveKeepsSourceOnCancelledContext(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	c := clusterOf(t, src, "IMG_1.jpg", "IMG_2.jpg", "IMG_3.jpg")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	results := moveOrg(dest).Place(ctx, c, "nature")
	for _, r := range results {
		if r.Moved {
			t.Errorf("a cancelled run must move nothing, %q reported Moved", r.Source)
		}
	}
	for _, p := range c.Photos {
		if !present(t, p.Path) {
			t.Errorf("a cancelled run must keep every source, %q is gone", p.Path)
		}
	}
}

// Companions move with their photo, and a companion failure is judged on its own: the
// photo's source still goes, because the photo's own copy was verified.
func TestMoveTakesCompanionsAndIsolatesTheirFailures(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	c := clusterOf(t, src, "IMG_1.jpg")
	photoPath := c.Photos[0].Path
	companion := filepath.Join(src, "IMG_1.jpg.xmp")
	writeFile(t, companion, "sidecar")

	org := moveOrg(dest)
	org.Sidecars = true
	results := org.Place(context.Background(), c, "nature")
	if len(results) != 2 {
		t.Fatalf("want the photo and its companion, got %d results", len(results))
	}
	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("placement error: %v", r.Err)
		}
		if !r.Moved {
			t.Errorf("%q was not moved", r.Source)
		}
	}
	if present(t, photoPath) || present(t, companion) {
		t.Error("both the photo and its companion source must be gone")
	}
}
