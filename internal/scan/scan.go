// Package scan walks a source directory and lists the image files moraine can
// sort, excluding the destination root so already-sorted photos are never
// re-ingested (I1 / FR-021).
//
// Symlinks are never traversed, and the rule is asymmetric by construction:
// filepath.WalkDir uses Lstat, so a symlinked *directory* is not a directory entry
// and is never descended into (it is reported at debug level rather than dropped in
// silence), while a symlinked *file* whose name carries a recognised extension is
// listed and read like any other photo — only its target's bytes are ever copied.
package scan

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/sgaunet/moraine/internal/photo"
)

// Found is a recognised image file discovered by the scan.
type Found struct {
	Path   string
	Format photo.Format
	// Size is the file's size in bytes, 0 when the walk could not stat it. It feeds
	// the run's free-space estimate; nothing in the pipeline depends on it being
	// exact, which is why an unstattable file is still reported rather than dropped.
	Size int64
}

// Scan recursively walks source and returns the recognised image files
// (JPEG/PNG/HEIC, case-insensitive). The destRoot directory — even when nested
// under source (e.g. "_sorted") — is skipped entirely.
//
// An unreadable subdirectory, or a file that vanishes mid-walk, is logged and
// skipped rather than aborting the walk: a single bad entry must not cost the
// whole run (FR-012). Only an unreadable source root is fatal.
//
// Cancelling ctx stops the walk at the next entry and returns the context error on
// its own, with no partial list: on a large library this stage is often the longest
// of the whole run, and it is precisely when a user notices they pointed at the
// wrong folder (Constitution Principle VIII).
func Scan(ctx context.Context, source, destRoot string, logger *slog.Logger) ([]Found, error) {
	cleanSource := filepath.Clean(source)
	isDest := destMatcher(destRoot)
	var found []Found

	err := filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr // stop promptly on cancellation
		}
		if err != nil {
			if filepath.Clean(path) == cleanSource {
				return err // the source root itself is unreadable: fatal
			}
			logger.Warn("path skipped", "path", path, "err", err)
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if isDest(path, d) {
				return fs.SkipDir // never descend into the destination root
			}
			return nil
		}
		format, ok := photo.FormatFromExt(d.Name())
		if !ok {
			// A symlinked directory arrives here rather than in the branch above,
			// since WalkDir stats with Lstat. Say so instead of dropping it silently.
			if d.Type()&fs.ModeSymlink != 0 {
				logger.Debug("symlink not followed", "path", path)
			}
			return nil
		}
		// One extra lstat per recognised file. It is invisible next to the full open
		// and EXIF read every one of these files gets a moment later, and it buys the
		// run its only chance to notice a too-small destination before writing.
		var size int64
		if info, ierr := d.Info(); ierr == nil {
			size = info.Size()
		} else {
			logger.Debug("size unknown", "path", path, "err", ierr)
		}
		found = append(found, Found{Path: path, Format: format, Size: size})
		return nil
	})
	if err != nil {
		// A cancellation is not a walk failure: return it bare, so callers match it
		// with errors.Is and report the interrupt rather than an unreadable source.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("walking source directory %q: %w", source, err)
	}
	return found, nil
}

// destMatcher builds the "is this walked directory the destination root?" test,
// resolving the destination once before the walk.
//
// Cleaned-string equality alone is not identity: a destination reached through a
// symlink (/tmp → /private/tmp on macOS) or spelled with different case on a
// case-insensitive filesystem would not match, the walk would descend into it, and
// an earlier run's copies would be re-ingested as if they were new photos. os.Stat
// follows symlinks, so its FileInfo is the real directory's — the one the walk meets
// under the source — and os.SameFile compares that identity. A destination that does
// not exist yet (the first run) has no identity to compare, and the string test is
// then all there is.
func destMatcher(destRoot string) func(path string, d fs.DirEntry) bool {
	cleanDest := filepath.Clean(destRoot)
	destInfo, _ := os.Stat(destRoot) // nil until the destination exists
	return func(path string, d fs.DirEntry) bool {
		if filepath.Clean(path) == cleanDest {
			return true
		}
		if destInfo == nil {
			return false
		}
		info, err := d.Info()
		return err == nil && os.SameFile(destInfo, info)
	}
}
