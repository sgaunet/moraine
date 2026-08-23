// Package scan walks a source directory and lists the image files moraine can
// sort, excluding the destination root so already-sorted photos are never
// re-ingested (I1 / FR-021).
package scan

import (
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"

	"github.com/sgaunet/moraine/internal/photo"
)

// Found is a recognised image file discovered by the scan.
type Found struct {
	Path   string
	Format photo.Format
}

// Scan recursively walks source and returns the recognised image files
// (JPEG/PNG/HEIC, case-insensitive). The destRoot directory — even when nested
// under source (e.g. "_sorted") — is skipped entirely.
//
// An unreadable subdirectory, or a file that vanishes mid-walk, is logged and
// skipped rather than aborting the walk: a single bad entry must not cost the
// whole run (FR-012). Only an unreadable source root is fatal.
func Scan(source, destRoot string, logger *slog.Logger) ([]Found, error) {
	cleanDest := filepath.Clean(destRoot)
	cleanSource := filepath.Clean(source)
	var found []Found

	err := filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
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
			if filepath.Clean(path) == cleanDest {
				return fs.SkipDir // never descend into the destination root
			}
			return nil
		}
		if format, ok := photo.FormatFromExt(d.Name()); ok {
			found = append(found, Found{Path: path, Format: format})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking source directory %q: %w", source, err)
	}
	return found, nil
}
