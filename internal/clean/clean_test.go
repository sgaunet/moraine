package clean_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sgaunet/moraine/internal/clean"
)

// writeFile creates path (and parents) with the given content.
func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// runClean executes a Cleaner, collecting each Result keyed by path.
func runClean(t *testing.T, c *clean.Cleaner) (clean.Summary, map[string]clean.Result) {
	t.Helper()
	results := make(map[string]clean.Result)
	sum, err := c.Run(context.Background(), func(r clean.Result) { results[r.Path] = r })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return sum, results
}

func assertDecision(t *testing.T, res map[string]clean.Result, path string, want clean.Decision) {
	t.Helper()
	r, ok := res[path]
	if !ok {
		t.Fatalf("no result recorded for %s", path)
	}
	if r.Decision != want {
		t.Errorf("%s: decision = %q, want %q (reason %q)", path, r.Decision, want, r.Reason)
	}
}

func assertExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Errorf("expected %s to exist: %v", path, err)
	}
}

func assertGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err == nil {
		t.Errorf("expected %s to have been deleted", path)
	}
}

// TestDeleteModeMatching covers US1: content (not name) matching, suffix-renamed and
// duplicate copies, uncopied retention, and the destination tree staying intact even
// though it is nested inside the source (overlap).
func TestDeleteModeMatching(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(src, "_sorted")

	copied := filepath.Join(src, "IMG_0001.jpg")
	suffixOrig := filepath.Join(src, "IMG_0002.jpg")
	dup := filepath.Join(src, "dup", "again.jpg")
	video := filepath.Join(src, "CLIP.mov")
	destCopyA := filepath.Join(dst, "family", "2025", "2025-08-12", "IMG_0001.jpg")
	destCopyB := filepath.Join(dst, "family", "2025", "2025-08-12", "IMG_0002 (1).jpg")

	writeFile(t, copied, []byte("PHOTO-A"))      // 7 bytes
	writeFile(t, suffixOrig, []byte("PHOTO-B"))  // 7 bytes, copy stored under a suffixed name
	writeFile(t, dup, []byte("PHOTO-A"))         // duplicate of A, also archived
	writeFile(t, video, []byte("MOVIE-BYTES11")) // not present in dest
	writeFile(t, destCopyA, []byte("PHOTO-A"))
	writeFile(t, destCopyB, []byte("PHOTO-B"))

	c := &clean.Cleaner{Source: src, DestRoot: dst, Delete: true}
	sum, res := runClean(t, c)

	assertDecision(t, res, copied, clean.DecisionDeleted)
	assertDecision(t, res, suffixOrig, clean.DecisionDeleted) // matched by content despite name diff
	assertDecision(t, res, dup, clean.DecisionDeleted)
	assertDecision(t, res, video, clean.DecisionKept)

	assertGone(t, copied)
	assertGone(t, suffixOrig)
	assertExists(t, video)
	assertExists(t, destCopyA) // destination untouched (overlap safety)
	assertExists(t, destCopyB)

	if sum.Deleted != 3 {
		t.Errorf("Deleted = %d, want 3", sum.Deleted)
	}
	if sum.Kept != 1 {
		t.Errorf("Kept = %d, want 1", sum.Kept)
	}
	if sum.SourceFilesHashed == 0 {
		t.Error("expected some source files to be hashed when sizes collide")
	}
}

// TestZeroByteCoincidentalMatch covers the 2026-06-26 clarification: matching is
// purely by content with no type guard, so a zero-byte non-photo is deleted when any
// zero-byte file exists in the destination.
func TestZeroByteCoincidentalMatch(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(src, "_sorted")
	empty := filepath.Join(src, "notes.txt")
	writeFile(t, empty, []byte{})
	writeFile(t, filepath.Join(dst, "x", "empty.jpg"), []byte{})

	c := &clean.Cleaner{Source: src, DestRoot: dst, Delete: true}
	_, res := runClean(t, c)

	assertDecision(t, res, empty, clean.DecisionDeleted)
	assertGone(t, empty)
}

// TestSizePrefilterNoCollision covers SC-007: with no shared sizes between source
// and destination, zero content hashes are computed and nothing is touched.
func TestSizePrefilterNoCollision(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(src, "_sorted")
	a := filepath.Join(src, "a.jpg")
	writeFile(t, a, []byte("AAAA"))                              // 4 bytes
	writeFile(t, filepath.Join(src, "b.jpg"), []byte("BBBBBB"))  // 6 bytes
	writeFile(t, filepath.Join(dst, "c.jpg"), []byte("CCCCCCC")) // 7 bytes — no source match

	c := &clean.Cleaner{Source: src, DestRoot: dst, Delete: false}
	sum, res := runClean(t, c)

	if sum.SourceFilesHashed != 0 {
		t.Errorf("SourceFilesHashed = %d, want 0 (no size collisions)", sum.SourceFilesHashed)
	}
	if sum.DestFilesHashed != 0 {
		t.Errorf("DestFilesHashed = %d, want 0 (no size collisions)", sum.DestFilesHashed)
	}
	assertDecision(t, res, a, clean.DecisionKept)
}

// TestKeptFilesUnchangedAndSymlinkSkipped covers FR-013 (no mutation of retained
// files) and the symlink/special-file edge (never followed or deleted).
func TestKeptFilesUnchangedAndSymlinkSkipped(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(src, "_sorted")
	keep := filepath.Join(src, "keep.mov")
	writeFile(t, keep, []byte("VIDEO"))
	writeFile(t, filepath.Join(dst, "p.jpg"), []byte("PIC"))

	link := filepath.Join(src, "link.jpg")
	if err := os.Symlink(keep, link); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}

	c := &clean.Cleaner{Source: src, DestRoot: dst, Delete: true}
	_, res := runClean(t, c)

	if _, ok := res[link]; ok {
		t.Error("symlink should never be evaluated")
	}
	assertExists(t, link)
	assertExists(t, keep)
	if got, _ := os.ReadFile(keep); string(got) != "VIDEO" {
		t.Errorf("retained file was mutated: %q", got)
	}
}

// TestDryRunNoMutationAndEquivalence covers US2/SC-002/FR-006: dry-run deletes
// nothing and its plan equals the set a delete run removes.
func TestDryRunNoMutationAndEquivalence(t *testing.T) {
	build := func(t *testing.T) (string, string, string) {
		t.Helper()
		src := t.TempDir()
		dst := filepath.Join(src, "_sorted")
		orig := filepath.Join(src, "a.jpg")
		writeFile(t, orig, []byte("PHOTO-A"))
		writeFile(t, filepath.Join(dst, "a.jpg"), []byte("PHOTO-A"))
		writeFile(t, filepath.Join(src, "b.mov"), []byte("VIDEO"))
		return src, dst, orig
	}

	src, dst, orig := build(t)
	drySum, dryRes := runClean(t, &clean.Cleaner{Source: src, DestRoot: dst, Delete: false})
	assertDecision(t, dryRes, orig, clean.DecisionWouldDelete)
	assertExists(t, orig) // dry-run mutates nothing
	if drySum.WouldDelete != 1 || drySum.Deleted != 0 {
		t.Errorf("dry-run summary = %+v, want WouldDelete=1 Deleted=0", drySum)
	}

	src2, dst2, orig2 := build(t)
	delSum, delRes := runClean(t, &clean.Cleaner{Source: src2, DestRoot: dst2, Delete: true})
	assertDecision(t, delRes, orig2, clean.DecisionDeleted)
	if delSum.Deleted != drySum.WouldDelete {
		t.Errorf("delete set (%d) != dry-run plan (%d)", delSum.Deleted, drySum.WouldDelete)
	}
}

// TestUnreadableSourceRetained covers FR-009/SC-005: a source file that cannot be
// hashed is retained as an error and the run continues over the rest.
func TestUnreadableSourceRetained(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(src, "_sorted")
	writeFile(t, filepath.Join(dst, "p.jpg"), []byte("PIC")) // 3 bytes
	bad := filepath.Join(src, "bad.jpg")
	good := filepath.Join(src, "ok.mov")
	writeFile(t, bad, []byte("PIC")) // same size → would be hashed
	writeFile(t, good, []byte("VIDEO"))

	if err := os.Chmod(bad, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0o644) })
	if f, err := os.Open(bad); err == nil { // running as root: perms not enforced
		_ = f.Close()
		t.Skip("file still readable despite chmod 000 (running as root?)")
	}

	sum, res := runClean(t, &clean.Cleaner{Source: src, DestRoot: dst, Delete: true})

	if res[bad].Decision != clean.DecisionError {
		t.Errorf("bad file decision = %q, want error", res[bad].Decision)
	}
	assertExists(t, bad) // retained (fail-safe)
	if sum.Errors < 1 {
		t.Errorf("Errors = %d, want >= 1", sum.Errors)
	}
	assertDecision(t, res, good, clean.DecisionKept) // run continued
}

// TestDeleteFailureReported covers FR-009: a matched original that cannot be removed
// is reported and left in place; the run continues.
func TestDeleteFailureReported(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(src, "_sorted")
	writeFile(t, filepath.Join(dst, "p.jpg"), []byte("PIC"))
	locked := filepath.Join(src, "locked")
	matched := filepath.Join(locked, "m.jpg")
	writeFile(t, matched, []byte("PIC")) // matches the destination copy

	if err := os.Chmod(locked, 0o555); err != nil { // read+exec, no write → Remove denied
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	probe := filepath.Join(locked, ".probe")
	if err := os.WriteFile(probe, []byte("x"), 0o644); err == nil { // running as root
		_ = os.Remove(probe)
		t.Skip("directory still writable despite chmod 555 (running as root?)")
	}

	_, res := runClean(t, &clean.Cleaner{Source: src, DestRoot: dst, Delete: true})

	if res[matched].Decision != clean.DecisionError {
		t.Errorf("decision = %q, want error", res[matched].Decision)
	}
	assertExists(t, matched) // file remains
}

// TestDestUnreadableUncertainKeep covers FR-009: when a same-size destination file
// cannot be hashed, the source original is retained as "uncertain".
func TestDestUnreadableUncertainKeep(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(src, "_sorted")
	source := filepath.Join(src, "s.jpg")
	writeFile(t, source, []byte("XYZ")) // 3 bytes
	badDest := filepath.Join(dst, "d.jpg")
	writeFile(t, badDest, []byte("ABC")) // same size, different content, made unreadable

	if err := os.Chmod(badDest, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(badDest, 0o644) })
	if f, err := os.Open(badDest); err == nil {
		_ = f.Close()
		t.Skip("file still readable despite chmod 000 (running as root?)")
	}

	_, res := runClean(t, &clean.Cleaner{Source: src, DestRoot: dst, Delete: true})

	r := res[source]
	if r.Decision != clean.DecisionKept {
		t.Errorf("decision = %q, want kept", r.Decision)
	}
	if !strings.Contains(r.Reason, "uncertain") {
		t.Errorf("reason = %q, want it to mention uncertainty", r.Reason)
	}
	assertExists(t, source)
}

// TestCancellation covers FR-012: a cancelled context stops the run promptly,
// returns the context error, and deletes nothing.
func TestCancellation(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(src, "_sorted")
	orig := filepath.Join(src, "a.jpg")
	writeFile(t, orig, []byte("PHOTO-A"))
	writeFile(t, filepath.Join(dst, "a.jpg"), []byte("PHOTO-A"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel: the walk must stop before deleting anything

	c := &clean.Cleaner{Source: src, DestRoot: dst, Delete: true}
	sum, err := c.Run(ctx, func(clean.Result) {})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if sum.Deleted != 0 {
		t.Errorf("Deleted = %d, want 0 after cancellation", sum.Deleted)
	}
	assertExists(t, orig) // matched candidate left on disk
}
