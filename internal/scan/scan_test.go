package scan_test

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/sgaunet/moraine/internal/photo"
	"github.com/sgaunet/moraine/internal/scan"
)

func write(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestScanRecursiveAndFilters(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "a.jpg"))
	write(t, filepath.Join(src, "b.JPEG"))
	write(t, filepath.Join(src, "sub", "c.png"))
	write(t, filepath.Join(src, "sub", "deep", "d.HEIC"))
	write(t, filepath.Join(src, "e.heif"))
	write(t, filepath.Join(src, "notes.txt")) // ignored
	write(t, filepath.Join(src, "movie.mp4")) // ignored
	write(t, filepath.Join(src, "raw.cr2"))   // ignored

	dest := filepath.Join(src, "_trie")

	found, err := scan.Scan(src, dest)
	if err != nil {
		t.Fatal(err)
	}
	names := relNames(t, src, found)
	want := []string{"a.jpg", "b.JPEG", "e.heif", "sub/c.png", "sub/deep/d.HEIC"}
	if !equal(names, want) {
		t.Fatalf("found %v; want %v", names, want)
	}
}

func TestScanExcludesDestRootUnderSource(t *testing.T) {
	src := t.TempDir()
	dest := filepath.Join(src, "_trie") // destination nested under source

	write(t, filepath.Join(src, "keep.jpg"))
	// Already-sorted photos living under _trie must be ignored.
	write(t, filepath.Join(dest, "voyage", "old1.jpg"))
	write(t, filepath.Join(dest, "old2.png"))

	found, err := scan.Scan(src, dest)
	if err != nil {
		t.Fatal(err)
	}
	names := relNames(t, src, found)
	if !equal(names, []string{"keep.jpg"}) {
		t.Fatalf("found %v; want only keep.jpg (destRoot excluded)", names)
	}
}

func TestScanFormatsClassified(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "p.heic"))
	found, err := scan.Scan(src, filepath.Join(src, "_trie"))
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Format != photo.HEIC {
		t.Fatalf("found %+v; want one HEIC", found)
	}
}

func relNames(t *testing.T, root string, found []scan.Found) []string {
	t.Helper()
	out := make([]string, 0, len(found))
	for _, f := range found {
		rel, err := filepath.Rel(root, f.Path)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, filepath.ToSlash(rel))
	}
	sort.Strings(out)
	return out
}

func equal(a, b []string) bool {
	sort.Strings(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
