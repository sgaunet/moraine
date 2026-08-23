// Package undo reverses one `sort` run: it removes the files that run created,
// and nothing else. Its authority is the run manifest — a file is a candidate only
// because a record says this run copied it there, and it is removed only while it
// still matches the size and modification time recorded for it. Anything the run
// merely found already in place, anything edited since, and anything outside the
// destination root is kept.
//
// It is the mirror image of `clean`: clean deletes *originals* that are safely
// archived, undo deletes *copies* that should not have been made. Both are
// dry-run by default. Pure filesystem logic — no transport, no global state
// (Constitution Principle III).
package undo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sgaunet/moraine/internal/manifest"
)

// Decision is the per-file outcome of an undo run.
type Decision string

const (
	// DecisionRemoved means the copy was removed.
	DecisionRemoved Decision = "removed"
	// DecisionWouldRemove means the copy is a candidate but nothing was removed (dry run).
	DecisionWouldRemove Decision = "would-remove"
	// DecisionKept means the file was retained (see Result.Reason).
	DecisionKept Decision = "kept"
	// DecisionError means the file could not be evaluated or removed; it was retained.
	DecisionError Decision = "error"
)

// The actions a manifest record can carry that mean "this run created this file".
// A record of any other action describes a file the run did not create.
const (
	actionCopied  = "copied"
	actionRenamed = "renamed"
)

// Reasons attached to a Result, kept stable for logs and tests.
const (
	reasonRunCreated  = "copied by this run"
	reasonNotCreated  = "not created by this run; only recognised"
	reasonMissing     = "already gone"
	reasonChanged     = "changed since the run"
	reasonNotRegular  = "no longer a regular file"
	reasonOutsideDest = "outside the destination root; never removed"
	reasonMoved       = "source was moved, not copied; this is the only remaining file"
)

// Result is the outcome for one recorded file. Records that placed nothing (a
// failed placement) never produce a Result.
type Result struct {
	Path     string   // absolute destination path from the manifest
	Decision Decision // removed | would-remove | kept | error
	Reason   string   // human-readable explanation
	Err      error    // non-nil on DecisionError
}

// Summary tallies an undo run for the final report and for tests.
type Summary struct {
	Removed     int // copies removed (delete mode)
	WouldRemove int // candidates (dry-run mode)
	Kept        int // files retained (any reason)
	Errors      int // files that errored (retained)
	DirsPruned  int // directories removed because the undo emptied them
}

// Undoer removes (or, in dry-run, would remove) the copies recorded by one run.
type Undoer struct {
	DestRoot string // absolute destination root; nothing outside it is ever touched
	Delete   bool   // false ⇒ dry-run (report only); true ⇒ perform removals
}

// Run walks the run's records — newest placement first, so a run is unwound in the
// order it was made — and calls onResult for each file it evaluates. Per-file
// failures are non-fatal (recorded as DecisionError, file retained). A cancelled
// context stops promptly and returns the context error with the partial Summary.
//
// After a complete delete-mode pass with no errors the manifest is marked undone,
// so the next `undo` steps back to the run before it instead of re-reporting a run
// whose files are already gone.
func (u *Undoer) Run(ctx context.Context, run manifest.Run, onResult func(Result)) (Summary, error) {
	var sum Summary
	if onResult == nil {
		onResult = func(Result) {}
	}
	cleanDest := filepath.Clean(u.DestRoot)
	emptied := make(map[string]struct{})

	for i := len(run.Records) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return sum, err
		}
		rec := run.Records[i]
		if rec.Dest == "" || rec.Error != "" {
			continue // this record placed no file
		}
		res := u.evaluate(rec, cleanDest)
		if res.Decision == DecisionRemoved {
			emptied[filepath.Dir(rec.Dest)] = struct{}{}
		}
		tally(&sum, res)
		onResult(res)
	}

	sum.DirsPruned = u.prune(emptied, cleanDest)
	if u.Delete && sum.Errors == 0 && run.Path != "" {
		if err := manifest.MarkUndone(run.Path); err != nil {
			return sum, err
		}
	}
	return sum, nil
}

// evaluate decides the fate of one recorded file.
func (u *Undoer) evaluate(rec manifest.Record, cleanDest string) Result {
	// A --move run is not reversible: removing this copy would destroy the only file
	// left, since the original no longer exists. Checked before the action, because a
	// moved file was still "copied" or "renamed" as far as the action goes.
	if rec.Moved {
		return Result{Path: rec.Dest, Decision: DecisionKept, Reason: reasonMoved}
	}
	if rec.Action != actionCopied && rec.Action != actionRenamed {
		// Chiefly "skipped-identical": the file was already there before the run.
		return Result{Path: rec.Dest, Decision: DecisionKept, Reason: reasonNotCreated}
	}
	if !withinDest(rec.Dest, cleanDest) {
		// A manifest read from another destination, or a tampered one. Never act on it.
		return Result{Path: rec.Dest, Decision: DecisionKept, Reason: reasonOutsideDest}
	}
	info, err := os.Lstat(rec.Dest)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{Path: rec.Dest, Decision: DecisionKept, Reason: reasonMissing}
		}
		return Result{Path: rec.Dest, Decision: DecisionError, Reason: "unreadable", Err: err}
	}
	if !info.Mode().IsRegular() {
		return Result{Path: rec.Dest, Decision: DecisionKept, Reason: reasonNotRegular}
	}
	if info.Size() != rec.Size || info.ModTime().UnixNano() != rec.MTime {
		// Someone edited or replaced the copy since the run: it is no longer the file
		// the run made, so removing it would destroy work moraine did not do.
		return Result{Path: rec.Dest, Decision: DecisionKept, Reason: reasonChanged}
	}
	if !u.Delete {
		return Result{Path: rec.Dest, Decision: DecisionWouldRemove, Reason: reasonRunCreated}
	}
	if err := os.Remove(rec.Dest); err != nil {
		return Result{
			Path: rec.Dest, Decision: DecisionError, Reason: "remove failed",
			Err: fmt.Errorf("removing %q: %w", rec.Dest, err),
		}
	}
	return Result{Path: rec.Dest, Decision: DecisionRemoved, Reason: reasonRunCreated}
}

// prune removes the directories the undo emptied, walking up from each towards
// (but never reaching) the destination root. os.Remove refuses a non-empty
// directory, which is exactly the guard needed: a folder still holding photos is
// left alone without having to list it.
func (u *Undoer) prune(dirs map[string]struct{}, cleanDest string) int {
	if !u.Delete {
		return 0
	}
	pruned := 0
	for dir := range dirs {
		for d := filepath.Clean(dir); d != cleanDest && withinDest(d, cleanDest); d = filepath.Dir(d) {
			if err := os.Remove(d); err != nil {
				break // not empty (or not ours): stop climbing this branch
			}
			pruned++
		}
	}
	return pruned
}

// tally records one Result into the summary.
func tally(sum *Summary, r Result) {
	switch r.Decision {
	case DecisionRemoved:
		sum.Removed++
	case DecisionWouldRemove:
		sum.WouldRemove++
	case DecisionKept:
		sum.Kept++
	case DecisionError:
		sum.Errors++
	}
}

// withinDest reports whether path lies within (or is) the cleaned destination root.
func withinDest(path, cleanDest string) bool {
	cp := filepath.Clean(path)
	if cp == cleanDest {
		return true
	}
	rel, err := filepath.Rel(cleanDest, cp)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
