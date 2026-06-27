// Package clean implements the `clean` subcommand's filesystem logic: it deletes
// source originals whose byte-identical copy already exists under the destination,
// matching purely by SHA-256 content (never by filename) and never touching the
// destination tree. The default is a dry run; deletion happens only when Delete is
// set. Pure business logic — no transport, no global state (Constitution Principle
// III). It depends on neither the classifier, the vision model, nor exiftool: only
// the filesystem and content hashing (FR-014).
package clean

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/sgaunet/moraine/internal/contenthash"
)

// Decision is the per-source-file outcome of a clean run.
type Decision string

const (
	// DecisionDeleted means an identical copy was found and the original was removed.
	DecisionDeleted Decision = "deleted"
	// DecisionWouldDelete means an identical copy was found but nothing was removed (dry run).
	DecisionWouldDelete Decision = "would-delete"
	// DecisionKept means the original was retained (see Result.Reason).
	DecisionKept Decision = "kept"
	// DecisionError means the file could not be evaluated or deleted; it was retained.
	DecisionError Decision = "error"
)

// Reasons attached to a Result, kept stable for logs and tests.
const (
	reasonCopyFound  = "identical copy found"
	reasonNoCopy     = "no copy found"
	reasonUncertain  = "uncertain: destination unreadable"
	reasonInsideDest = "inside destination tree; never deleted"
)

// Result is the outcome for one source file. Entries skipped before evaluation
// (directories, the destination subtree, symlinks and other non-regular files)
// never produce a Result.
type Result struct {
	Path     string   // absolute source path
	Decision Decision // deleted | would-delete | kept | error
	Reason   string   // human-readable explanation
	Err      error    // non-nil on DecisionError (could not evaluate, or delete failed)
}

// Summary tallies a clean run for the final log line and for tests.
type Summary struct {
	Deleted           int // originals removed (delete mode)
	WouldDelete       int // candidates (dry-run mode)
	Kept              int // originals retained (any reason)
	Errors            int // files that errored (retained)
	SourceFilesHashed int // source files for which a SHA-256 was computed (asserts SC-007)
	DestFilesHashed   int // destination files hashed, lazily (symmetric half of FR-015)
}

// Cleaner deletes (or, in dry-run, would delete) source originals that have a
// byte-identical copy under DestRoot.
type Cleaner struct {
	Source   string // absolute source root to clean
	DestRoot string // absolute destination root (the archive); never deleted from
	Delete   bool   // false ⇒ dry-run (report only); true ⇒ perform deletions
}

// Run evaluates every regular file under Source and, for those whose byte-identical
// content exists under DestRoot, deletes them (when Delete is true) or reports them
// as would-delete (dry run). It calls onResult for each evaluated file and returns
// an aggregate Summary. Per-file failures are non-fatal (recorded as DecisionError,
// original retained). Context cancellation stops the walk promptly and returns the
// context error together with the partial Summary.
func (c *Cleaner) Run(ctx context.Context, onResult func(Result)) (Summary, error) {
	var sum Summary
	if onResult == nil {
		onResult = func(Result) {}
	}

	idx, err := c.indexDestination()
	if err != nil {
		return sum, err
	}

	cleanDest := filepath.Clean(c.DestRoot)
	emit := func(r Result) {
		onResult(r)
		tally(&sum, r)
	}

	walkErr := filepath.WalkDir(c.Source, func(path string, d fs.DirEntry, walkErr error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr // stop promptly on cancellation
		}
		if walkErr != nil {
			// Unreadable entry: record and continue (skip the subtree if it is a dir).
			emit(Result{Path: path, Decision: DecisionError, Reason: "unreadable", Err: walkErr})
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if filepath.Clean(path) == cleanDest {
				return fs.SkipDir // never descend into the destination tree
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil // symlinks and special files are never followed or deleted
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			emit(Result{Path: path, Decision: DecisionError, Reason: "unreadable", Err: fmt.Errorf("stat %q: %w", path, infoErr)})
			return nil
		}
		emit(c.evaluate(path, info.Size(), cleanDest, idx, &sum))
		return nil
	})
	if walkErr != nil {
		if errors.Is(walkErr, context.Canceled) || errors.Is(walkErr, context.DeadlineExceeded) {
			return sum, walkErr
		}
		return sum, fmt.Errorf("walking source %q: %w", c.Source, walkErr)
	}
	return sum, nil
}

// evaluate decides the fate of one regular source file. It updates only the hash
// counters on sum; the decision itself is tallied by the caller.
func (c *Cleaner) evaluate(path string, size int64, cleanDest string, idx *destIndex, sum *Summary) Result {
	if len(idx.bySize[size]) == 0 {
		return Result{Path: path, Decision: DecisionKept, Reason: reasonNoCopy} // no size match → never hashed
	}
	srcSum, err := contenthash.Hash(path)
	if err != nil {
		return Result{Path: path, Decision: DecisionError, Reason: "source unreadable", Err: err}
	}
	sum.SourceFilesHashed++

	hashes, anyUnreadable := idx.hashesForSize(size, sum)
	if _, ok := hashes[srcSum]; ok {
		return c.matched(path, cleanDest)
	}
	if anyUnreadable {
		// A same-size destination file could not be hashed, so we cannot rule out
		// that it is the copy: retain the original (fail-safe).
		return Result{Path: path, Decision: DecisionKept, Reason: reasonUncertain}
	}
	return Result{Path: path, Decision: DecisionKept, Reason: reasonNoCopy}
}

// matched produces the outcome for a source file whose copy was confirmed: it
// removes the original in delete mode, or reports a would-delete in dry-run mode.
func (c *Cleaner) matched(path, cleanDest string) Result {
	if withinDest(path, cleanDest) {
		// Defense in depth: the walk already skips the destination subtree, but never
		// delete anything under it even if a path slipped through.
		return Result{Path: path, Decision: DecisionKept, Reason: reasonInsideDest}
	}
	if !c.Delete {
		return Result{Path: path, Decision: DecisionWouldDelete, Reason: reasonCopyFound}
	}
	if err := os.Remove(path); err != nil {
		return Result{Path: path, Decision: DecisionError, Reason: "delete failed", Err: fmt.Errorf("removing %q: %w", path, err)}
	}
	return Result{Path: path, Decision: DecisionDeleted, Reason: reasonCopyFound}
}

// tally records one Result into the summary.
func tally(sum *Summary, r Result) {
	switch r.Decision {
	case DecisionDeleted:
		sum.Deleted++
	case DecisionWouldDelete:
		sum.WouldDelete++
	case DecisionKept:
		sum.Kept++
	case DecisionError:
		sum.Errors++
	}
}

// destIndex maps destination file sizes to paths, hashing them lazily on demand.
type destIndex struct {
	bySize     map[int64][]string                     // size → destination paths (no hashing at build time)
	hashCache  map[int64]map[contenthash.Sum]struct{} // size → set of content sums (computed on first need)
	unreadable map[int64]bool                         // size → whether any same-size dest file could not be hashed
}

// indexDestination walks DestRoot and records each regular file's size, without
// hashing. A destination entry that cannot be read is skipped conservatively (it
// simply cannot serve as a match, which can only ever retain a source original).
func (c *Cleaner) indexDestination() (*destIndex, error) {
	idx := &destIndex{
		bySize:     make(map[int64][]string),
		hashCache:  make(map[int64]map[contenthash.Sum]struct{}),
		unreadable: make(map[int64]bool),
	}
	err := filepath.WalkDir(c.DestRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil // unknown size: cannot index; skip
		}
		idx.bySize[info.Size()] = append(idx.bySize[info.Size()], path)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scanning destination %q: %w", c.DestRoot, err)
	}
	return idx, nil
}

// hashesForSize returns the set of content sums of the destination files of the
// given size, hashing them lazily on first use and caching the result. The bool
// reports whether at least one same-size destination file could not be hashed
// (which drives the fail-safe "uncertain" retention). Destination files whose size
// never appears in the source are therefore never hashed (FR-015).
func (idx *destIndex) hashesForSize(size int64, sum *Summary) (map[contenthash.Sum]struct{}, bool) {
	if set, ok := idx.hashCache[size]; ok {
		return set, idx.unreadable[size]
	}
	set := make(map[contenthash.Sum]struct{})
	unreadable := false
	for _, p := range idx.bySize[size] {
		h, err := contenthash.Hash(p)
		if err != nil {
			unreadable = true
			continue
		}
		set[h] = struct{}{}
		sum.DestFilesHashed++
	}
	idx.hashCache[size] = set
	idx.unreadable[size] = unreadable
	return set, unreadable
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
