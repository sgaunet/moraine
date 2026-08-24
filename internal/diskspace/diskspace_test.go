//go:build unix

package diskspace_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sgaunet/moraine/internal/diskspace"
)

func TestAvailableOnExistingDirectory(t *testing.T) {
	avail, err := diskspace.Available(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if avail == 0 {
		t.Error("Available = 0 bytes on a writable temp directory; want a non-zero figure")
	}
}

// TestAvailableWalksUpToAnExistingAncestor pins the first-run case: moraine's
// destination root is created on first use, so the preflight almost always asks about
// a path that does not exist yet. The answer must be the hosting filesystem's, not an
// error.
func TestAvailableWalksUpToAnExistingAncestor(t *testing.T) {
	dir := t.TempDir()
	want, err := diskspace.Available(dir)
	if err != nil {
		t.Fatal(err)
	}

	got, err := diskspace.Available(filepath.Join(dir, "sorted", "a", "b"))
	if err != nil {
		t.Fatalf("Available on a not-yet-created destination: %v", err)
	}
	if got != want {
		t.Errorf("Available = %d for a missing path; want %d, its ancestor's filesystem", got, want)
	}
}

// The walk steps past a missing path, not past a broken one. A destination named
// underneath a regular file fails ENOTDIR, which is not fs.ErrNotExist, so it is
// reported instead of being answered for some ancestor — the path can never host a
// directory, and saying so is more useful than a free-space figure for it.
func TestAvailableUnderARegularFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := diskspace.Available(filepath.Join(file, "sorted")); err == nil {
		t.Error("Available returned no error for a path under a regular file")
	}
}
