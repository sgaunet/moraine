package undo_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/manifest"
	"github.com/sgaunet/moraine/internal/undo"
)

var runTime = time.Date(2026, 8, 23, 9, 12, 5, 0, time.UTC)

// placed writes a file at dest/rel and returns the record a run would have written
// for it, fingerprinted from the file itself.
func placed(t *testing.T, dest, rel, content, action string) manifest.Record {
	t.Helper()
	path := filepath.Join(dest, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return manifest.Record{
		Source: "/src/" + filepath.Base(rel), Dest: path, Action: action,
		Size: info.Size(), MTime: info.ModTime().UnixNano(),
	}
}

// recordRun writes a real manifest for the records and reads it back, so the tests
// exercise undo against the same file a sort run would have left behind.
func recordRun(t *testing.T, dest string, records ...manifest.Record) manifest.Run {
	t.Helper()
	w := manifest.New(dest, "/src", runTime)
	for _, r := range records {
		if err := w.Add(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	run, err := manifest.ReadRun(w.Path())
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	return err == nil
}

// TestUndoRemovesWhatTheRunCreated is the happy path: the copies a run made are
// removed, the folders they were the last occupant of are pruned, and the manifest
// is marked so a second undo steps back to the run before it.
func TestUndoRemovesWhatTheRunCreated(t *testing.T) {
	dest := t.TempDir()
	copied := placed(t, dest, "family/2026/2026-08-23/a.jpg", "aaa", "copied")
	renamed := placed(t, dest, "family/2026/2026-08-23/b (1).jpg", "bbb", "renamed")
	run := recordRun(t, dest, copied, renamed)

	u := &undo.Undoer{DestRoot: dest, Delete: true}
	var results []undo.Result
	sum, err := u.Run(context.Background(), run, func(r undo.Result) { results = append(results, r) })
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if sum.Removed != 2 || sum.Kept != 0 || sum.Errors != 0 {
		t.Fatalf("summary = %+v; want Removed=2", sum)
	}
	if len(results) != 2 {
		t.Errorf("results = %d, want one per record", len(results))
	}
	for _, rec := range []manifest.Record{copied, renamed} {
		if exists(t, rec.Dest) {
			t.Errorf("%s should have been removed", rec.Dest)
		}
	}
	if exists(t, filepath.Join(dest, "family")) {
		t.Error("the emptied theme folder should have been pruned")
	}
	if sum.DirsPruned == 0 {
		t.Error("DirsPruned = 0; want the emptied folders to be counted")
	}
	if !exists(t, dest) {
		t.Error("the destination root itself must never be removed")
	}
	// The run is spent: it must not be offered as the latest run any more.
	latest, err := manifest.Latest(dest)
	if err != nil {
		t.Fatal(err)
	}
	if latest != "" {
		t.Errorf("Latest() = %q after a completed undo, want no pending run", latest)
	}
	if !exists(t, run.Path+".undone") {
		t.Error("the manifest should be kept as an audit trail, renamed .undone")
	}
}

// TestUndoDryRunRemovesNothing: undo is a preview until --delete, like clean.
func TestUndoDryRunRemovesNothing(t *testing.T) {
	dest := t.TempDir()
	rec := placed(t, dest, "cook/2026/2026-08-23/a.jpg", "aaa", "copied")
	run := recordRun(t, dest, rec)

	sum, err := (&undo.Undoer{DestRoot: dest}).Run(context.Background(), run, nil)
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if sum.WouldRemove != 1 || sum.Removed != 0 || sum.DirsPruned != 0 {
		t.Fatalf("summary = %+v; want WouldRemove=1 and nothing touched", sum)
	}
	if !exists(t, rec.Dest) {
		t.Error("a dry run must not remove anything")
	}
	latest, err := manifest.Latest(dest)
	if err != nil {
		t.Fatal(err)
	}
	if latest != run.Path {
		t.Error("a dry run must leave the manifest pending, not mark it undone")
	}
}

// TestUndoKeepsFilesTheRunDidNotCreate is the safety rule that matters most: a
// file the run merely found already in place (skipped-identical) predates the run
// and is never its to remove.
func TestUndoKeepsFilesTheRunDidNotCreate(t *testing.T) {
	dest := t.TempDir()
	kept := placed(t, dest, "family/2026/2026-08-23/pre-existing.jpg", "aaa", "skipped-identical")
	run := recordRun(t, dest, kept)

	var results []undo.Result
	sum, err := (&undo.Undoer{DestRoot: dest, Delete: true}).Run(
		context.Background(), run, func(r undo.Result) { results = append(results, r) })
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if sum.Kept != 1 || sum.Removed != 0 {
		t.Fatalf("summary = %+v; want Kept=1 Removed=0", sum)
	}
	if !exists(t, kept.Dest) {
		t.Fatal("a file this run did not create must survive the undo")
	}
	if len(results) != 1 || results[0].Decision != undo.DecisionKept || results[0].Reason == "" {
		t.Errorf("result = %+v; want a kept decision with a reason", results)
	}
}

func TestUndoKeepsWhatNoLongerMatchesTheRecord(t *testing.T) {
	dest := t.TempDir()
	edited := placed(t, dest, "family/2026/2026-08-23/edited.jpg", "aaa", "copied")
	touched := placed(t, dest, "family/2026/2026-08-23/touched.jpg", "bbb", "copied")
	gone := placed(t, dest, "family/2026/2026-08-23/gone.jpg", "ccc", "copied")
	dir := placed(t, dest, "family/2026/2026-08-23/a-directory", "", "copied")
	run := recordRun(t, dest, edited, touched, gone, dir)

	// Content changed since the run.
	if err := os.WriteFile(edited.Dest, []byte("edited in place"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Same content, different modification time.
	later := time.Unix(0, touched.MTime).Add(time.Hour)
	if err := os.Chtimes(touched.Dest, later, later); err != nil {
		t.Fatal(err)
	}
	// Already removed by hand.
	if err := os.Remove(gone.Dest); err != nil {
		t.Fatal(err)
	}
	// A directory now stands where the recorded file was.
	if err := os.Remove(dir.Dest); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir.Dest, 0o755); err != nil {
		t.Fatal(err)
	}

	sum, err := (&undo.Undoer{DestRoot: dest, Delete: true}).Run(context.Background(), run, nil)
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if sum.Kept != 4 || sum.Removed != 0 {
		t.Fatalf("summary = %+v; want everything kept", sum)
	}
	for _, path := range []string{edited.Dest, touched.Dest, dir.Dest} {
		if !exists(t, path) {
			t.Errorf("%s no longer matches its record and must be kept", path)
		}
	}
}

func TestUndoRefusesPathsOutsideTheDestination(t *testing.T) {
	dest := t.TempDir()
	elsewhere := t.TempDir()
	outside := placed(t, elsewhere, "a.jpg", "aaa", "copied")
	run := recordRun(t, dest, outside)

	var results []undo.Result
	sum, err := (&undo.Undoer{DestRoot: dest, Delete: true}).Run(
		context.Background(), run, func(r undo.Result) { results = append(results, r) })
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if sum.Removed != 0 || sum.Kept != 1 {
		t.Fatalf("summary = %+v; want the outside path kept", sum)
	}
	if !exists(t, outside.Dest) {
		t.Fatal("undo must never remove anything outside the destination root")
	}
	if len(results) != 1 || !strings.Contains(results[0].Reason, "destination") {
		t.Errorf("result = %+v; want a reason naming the destination boundary", results)
	}
}

func TestUndoCancelledContextStopsAndReports(t *testing.T) {
	dest := t.TempDir()
	rec := placed(t, dest, "family/2026/2026-08-23/a.jpg", "aaa", "copied")
	run := recordRun(t, dest, rec)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sum, err := (&undo.Undoer{DestRoot: dest, Delete: true}).Run(ctx, run, nil)
	if err == nil {
		t.Fatal("a cancelled undo must report the cancellation")
	}
	if sum.Removed != 0 {
		t.Errorf("summary = %+v; want nothing removed", sum)
	}
	if !exists(t, rec.Dest) {
		t.Error("a cancelled undo must not have removed anything")
	}
	latest, err := manifest.Latest(dest)
	if err != nil {
		t.Fatal(err)
	}
	if latest != run.Path {
		t.Error("an interrupted undo must leave the run pending")
	}
}

// TestUndoSkipsRecordsThatPlacedNothing: a failed placement created no file, so it
// is not part of the undo at all.
func TestUndoSkipsRecordsThatPlacedNothing(t *testing.T) {
	dest := t.TempDir()
	run := recordRun(t, dest,
		manifest.Record{Source: "/src/a.jpg", Dest: filepath.Join(dest, "a.jpg"), Action: "copied", Error: "disk full"},
		manifest.Record{Source: "/src/b.jpg", Action: "copied"},
	)
	sum, err := (&undo.Undoer{DestRoot: dest, Delete: true}).Run(context.Background(), run, nil)
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if sum != (undo.Summary{}) {
		t.Errorf("summary = %+v; want an empty tally", sum)
	}
}

// TestUndoKeepsWhatAMoveRunProduced is the safety rule that makes --move and undo
// coexist: a moved file's original no longer exists, so its copy is the only
// remaining file. Removing it would destroy the user's data, which is the one thing
// undo must never do — so it keeps the copy and says why.
func TestUndoKeepsWhatAMoveRunProduced(t *testing.T) {
	dest := t.TempDir()
	moved := placed(t, dest, "family/2026/2026-08-23/a.jpg", "moved-bytes", "copied")
	moved.Moved = true
	movedRename := placed(t, dest, "family/2026/2026-08-23/b (1).jpg", "moved-too", "renamed")
	movedRename.Moved = true
	// A plain copy in the same run is still removable; nothing about --move is
	// recorded per run, only per file.
	copied := placed(t, dest, "family/2026/2026-08-23/c.jpg", "copied-bytes", "copied")
	run := recordRun(t, dest, moved, movedRename, copied)

	var results []undo.Result
	sum, err := (&undo.Undoer{DestRoot: dest, Delete: true}).Run(
		context.Background(), run, func(r undo.Result) { results = append(results, r) })
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if sum.Removed != 1 || sum.Kept != 2 {
		t.Fatalf("summary = %+v; want Removed=1 Kept=2", sum)
	}
	for _, rec := range []manifest.Record{moved, movedRename} {
		if !exists(t, rec.Dest) {
			t.Errorf("undo removed %q, but its source was moved — that was the only copy left", rec.Dest)
		}
	}
	if exists(t, copied.Dest) {
		t.Errorf("a plain copy in the same run should still have been removed: %q", copied.Dest)
	}
	// The reason must explain itself, since "kept" alone looks like a bug here.
	var explained int
	for _, r := range results {
		if r.Decision == undo.DecisionKept && strings.Contains(r.Reason, "moved") {
			explained++
		}
	}
	if explained != 2 {
		t.Errorf("both kept records must say the source was moved; got %d of 2", explained)
	}
}

// A dry-run undo over a move run must also report those files as kept, so the preview
// tells the truth about what a --delete pass would do.
func TestUndoDryRunReportsMovedFilesAsKept(t *testing.T) {
	dest := t.TempDir()
	moved := placed(t, dest, "family/2026/2026-08-23/a.jpg", "moved-bytes", "copied")
	moved.Moved = true
	run := recordRun(t, dest, moved)

	sum, err := (&undo.Undoer{DestRoot: dest}).Run(context.Background(), run, nil)
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if sum.WouldRemove != 0 || sum.Kept != 1 {
		t.Fatalf("summary = %+v; want WouldRemove=0 Kept=1", sum)
	}
}
