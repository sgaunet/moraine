// Package scan walks a source directory and lists the image files moraine can
// sort, excluding the destination root so already-sorted photos are never
// re-ingested (I1 / FR-021).
package scan

import (
	"fmt"
	"io/fs"
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
// under source (e.g. "_trie") — is skipped entirely.
func Scan(source, destRoot string) ([]Found, error) {
	cleanDest := filepath.Clean(destRoot)
	var found []Found

	err := filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
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
		return nil, fmt.Errorf("parcours du dossier source %q: %w", source, err)
	}
	return found, nil
}
