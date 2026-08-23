// Package organize places photos onto disk under a deterministic layout below the
// destination root — by default <theme>/<year>/<year-month-day>/, and otherwise
// whatever Template describes (see template.go). It only ever copies (originals are
// preserved) and never overwrites or loses a file: identical targets are skipped and
// same-named different content is suffixed. Business logic only — no transport or
// global state (Constitution Principle III).
package organize

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrInvalidDestSubdir is returned when a computed destination subdirectory
// would escape the destination root (anti-traversal).
var ErrInvalidDestSubdir = errors.New("invalid destination subdirectory")

// safeJoin resolves subdir under root and guarantees the result stays within
// root. Absolute subdirs and ".." escapes are rejected (anti-traversal).
func safeJoin(root, subdir string) (string, error) {
	if filepath.IsAbs(subdir) {
		return "", fmt.Errorf("%w: absolute path %q is not allowed", ErrInvalidDestSubdir, subdir)
	}
	joined := filepath.Join(root, subdir)
	rel, err := filepath.Rel(root, joined)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrInvalidDestSubdir, err.Error())
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q would escape the destination directory", ErrInvalidDestSubdir, subdir)
	}
	return joined, nil
}

// uniqueName returns a file name under dir that does not collide with a name
// already taken, suffixing " (1)", " (2)", … as needed. It never proposes a name
// that would overwrite an existing file.
//
// taken decides what "already taken" means. A real run passes exists — only files
// on disk count. A dry run also counts the names it has already promised to create
// this run, so a preview reports the same collision renames the real run would
// perform (nothing is written, so those names are not on disk to be found).
func uniqueName(dir, name string, taken func(string) bool) string {
	if !taken(filepath.Join(dir, name)) {
		return name
	}
	for i := 1; ; i++ {
		candidate := variantName(name, i)
		if !taken(filepath.Join(dir, candidate)) {
			return candidate
		}
	}
}

// existingIdentical returns the name of a file already in dir that is a collision
// variant of name (" (1)", " (2)", …) and byte-identical to src, or "" when there
// is none. It lets a re-run recognise content it already placed under a suffixed
// name instead of copying it again under the next free suffix.
//
// Because uniqueName always fills the first free index, the occupied indices are
// contiguous, so the scan stops at the first gap — no directory listing needed.
// This deliberately looks only at files on disk, never at a dry run's planned
// names: only real bytes can be compared for identity.
func existingIdentical(dir, name, src string) (string, error) {
	for i := 1; ; i++ {
		candidate := variantName(name, i)
		target := filepath.Join(dir, candidate)
		if !exists(target) {
			return "", nil
		}
		same, err := sameContent(src, target)
		if err != nil {
			return "", err
		}
		if same {
			return candidate, nil
		}
	}
}

// variantName builds the i-th collision variant of name ("stem (i)ext"). It is the
// single naming rule shared by uniqueName and existingIdentical, so the name a
// collision allocates and the name a re-run looks for can never drift apart.
func variantName(name string, i int) string {
	ext := filepath.Ext(name)
	return fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), i, ext)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
