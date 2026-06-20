package organize

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/photo"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSafeJoin(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name    string
		subdir  string
		wantErr bool
	}{
		{"simple", filepath.Join("mountain", "2025", "2025-08-12"), false},
		{"absolute rejected", string(filepath.Separator) + "etc", true},
		{"escape rejected", filepath.Join("..", "..", "evil"), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := safeJoin(root, tc.subdir)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				if !errors.Is(err, ErrInvalidDestSubdir) {
					t.Fatalf("expected ErrInvalidDestSubdir, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestUniqueName(t *testing.T) {
	dir := t.TempDir()
	if got := uniqueName(dir, "a.jpg"); got != "a.jpg" {
		t.Fatalf("no collision: want a.jpg, got %q", got)
	}
	writeFile(t, filepath.Join(dir, "a.jpg"), "x")
	if got := uniqueName(dir, "a.jpg"); got != "a (1).jpg" {
		t.Fatalf("first collision: want 'a (1).jpg', got %q", got)
	}
	writeFile(t, filepath.Join(dir, "a (1).jpg"), "x")
	if got := uniqueName(dir, "a.jpg"); got != "a (2).jpg" {
		t.Fatalf("second collision: want 'a (2).jpg', got %q", got)
	}
}

func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")
	writeFile(t, src, "hello")

	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "hello" {
		t.Fatalf("content: want hello, got %q", got)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("source must be preserved: %v", err)
	}
	// O_EXCL: refuse to overwrite an existing destination.
	if err := copyFile(src, dst); err == nil {
		t.Fatal("expected error overwriting existing dst")
	}
}

func TestSameContent(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	c := filepath.Join(dir, "c")
	writeFile(t, a, "same")
	writeFile(t, b, "same")
	writeFile(t, c, "different-length")

	if ok, err := sameContent(a, b); err != nil || !ok {
		t.Fatalf("identical: ok=%v err=%v", ok, err)
	}
	if ok, err := sameContent(a, c); err != nil || ok {
		t.Fatalf("size mismatch must be false: ok=%v err=%v", ok, err)
	}
	// same size, different bytes
	d := filepath.Join(dir, "d")
	writeFile(t, d, "samz")
	writeFile(t, a, "same")
	if ok, err := sameContent(a, d); err != nil || ok {
		t.Fatalf("different bytes must be false: ok=%v err=%v", ok, err)
	}
}

func clusterOf(t *testing.T, dir string, names ...string) photo.Cluster {
	t.Helper()
	date := time.Date(2025, 8, 12, 10, 0, 0, 0, time.UTC)
	var ps []photo.Photo
	for i, n := range names {
		p := filepath.Join(dir, n)
		writeFile(t, p, "content-"+n)
		ps = append(ps, photo.Photo{Path: p, Name: n, Taken: date.Add(time.Duration(i) * time.Minute), Format: photo.JPEG})
	}
	return photo.Cluster{Photos: ps, Start: date, End: date}
}

func TestPlaceCopiesIntoLayout(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	c := clusterOf(t, src, "IMG_1.jpg", "IMG_2.jpg")

	results := New(dest).Place(context.Background(), c, "nature")
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d", len(results))
	}
	wantDir := filepath.Join(dest, "nature", "2025", "2025-08-12")
	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("placement error: %v", r.Err)
		}
		if r.Action != ActionCopied {
			t.Fatalf("want copied, got %s", r.Action)
		}
		if filepath.Dir(r.Dest) != wantDir {
			t.Fatalf("want dir %q, got %q", wantDir, filepath.Dir(r.Dest))
		}
		if _, err := os.Stat(r.Dest); err != nil {
			t.Fatalf("dest missing: %v", err)
		}
		if _, err := os.Stat(r.Source); err != nil {
			t.Fatalf("source must be preserved: %v", err)
		}
	}
}

func TestPlaceSkipsIdenticalAndRenamesDifferent(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	c := clusterOf(t, src, "IMG_1.jpg")

	org := New(dest)
	first := org.Place(context.Background(), c, "family")
	if first[0].Action != ActionCopied {
		t.Fatalf("first run: want copied, got %s", first[0].Action)
	}

	// Re-run identical → skipped.
	second := org.Place(context.Background(), c, "family")
	if second[0].Action != ActionSkippedIdentical {
		t.Fatalf("re-run: want skipped-identical, got %s", second[0].Action)
	}

	// Different content, same name → renamed.
	writeFile(t, filepath.Join(src, "IMG_1.jpg"), "totally-different-bytes")
	third := org.Place(context.Background(), c, "family")
	if third[0].Action != ActionRenamed {
		t.Fatalf("different content: want renamed, got %s", third[0].Action)
	}
	if filepath.Base(third[0].Dest) != "IMG_1 (1).jpg" {
		t.Fatalf("want suffixed name, got %q", filepath.Base(third[0].Dest))
	}
}

func TestPlaceCancelledContext(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	c := clusterOf(t, src, "IMG_1.jpg")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results := New(dest).Place(ctx, c, "nature")
	if results[0].Err == nil {
		t.Fatal("expected context error")
	}
}
