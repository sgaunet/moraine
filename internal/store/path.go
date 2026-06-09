package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// safeJoin resolves subdir under root and guarantees the result stays within
// root (VR-1, anti-traversal). Absolute subdirs and ".." escapes are rejected.
func safeJoin(root, subdir string) (string, error) {
	if filepath.IsAbs(subdir) {
		return "", fmt.Errorf("%w: chemin absolu %q interdit", ErrInvalidDestSubdir, subdir)
	}
	joined := filepath.Join(root, subdir)
	rel, err := filepath.Rel(root, joined)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrInvalidDestSubdir, err.Error())
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q sortirait du répertoire de destination", ErrInvalidDestSubdir, subdir)
	}
	return joined, nil
}

// uniqueName returns a file name under dir that does not collide with an
// existing file, suffixing " (1)", " (2)", … as needed (VR-3 / I4). It never
// proposes a name that would overwrite an existing file.
func uniqueName(dir, name string) string {
	if !exists(filepath.Join(dir, name)) {
		return name
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; ; i++ {
		candidate := fmt.Sprintf("%s (%d)%s", stem, i, ext)
		if !exists(filepath.Join(dir, candidate)) {
			return candidate
		}
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
