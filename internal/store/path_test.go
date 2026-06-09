package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSafeJoin(t *testing.T) {
	root := filepath.FromSlash("/dest/root")
	ok := []struct {
		subdir string
		want   string
	}{
		{"voyage/italie", filepath.Join(root, "voyage", "italie")},
		{"", root},
		{"a/b/c", filepath.Join(root, "a", "b", "c")},
		{"voyage/../voyage2", filepath.Join(root, "voyage2")}, // stays inside
	}
	for _, tc := range ok {
		got, err := safeJoin(root, tc.subdir)
		if err != nil {
			t.Errorf("safeJoin(%q) unexpected error: %v", tc.subdir, err)
			continue
		}
		if got != tc.want {
			t.Errorf("safeJoin(%q) = %q; want %q", tc.subdir, got, tc.want)
		}
	}

	bad := []string{
		"../escape",
		"../../etc/passwd",
		"voyage/../../escape",
		filepath.FromSlash("/absolute/path"),
	}
	for _, subdir := range bad {
		if _, err := safeJoin(root, subdir); !errors.Is(err, ErrInvalidDestSubdir) {
			t.Errorf("safeJoin(%q) error = %v; want ErrInvalidDestSubdir", subdir, err)
		}
	}
}

func TestUniqueName(t *testing.T) {
	dir := t.TempDir()

	// No collision → original name.
	if got := uniqueName(dir, "IMG.jpg"); got != "IMG.jpg" {
		t.Errorf("uniqueName (no collision) = %q; want IMG.jpg", got)
	}

	// Create collisions and check suffixing.
	must(t, os.WriteFile(filepath.Join(dir, "IMG.jpg"), []byte("a"), 0o600))
	if got := uniqueName(dir, "IMG.jpg"); got != "IMG (1).jpg" {
		t.Errorf("uniqueName (1 collision) = %q; want 'IMG (1).jpg'", got)
	}

	must(t, os.WriteFile(filepath.Join(dir, "IMG (1).jpg"), []byte("b"), 0o600))
	if got := uniqueName(dir, "IMG.jpg"); got != "IMG (2).jpg" {
		t.Errorf("uniqueName (2 collisions) = %q; want 'IMG (2).jpg'", got)
	}

	// File without extension.
	must(t, os.WriteFile(filepath.Join(dir, "README"), []byte("c"), 0o600))
	if got := uniqueName(dir, "README"); got != "README (1)" {
		t.Errorf("uniqueName (no ext) = %q; want 'README (1)'", got)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
