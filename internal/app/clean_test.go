package app_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/sgaunet/moraine/internal/app"
	"github.com/sgaunet/moraine/internal/config"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func writeCleanFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCleanEndToEndDelete covers US1's independent test through the orchestrator:
// copied originals are removed, uncopied files remain, and the destination is
// untouched.
func TestCleanEndToEndDelete(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(src, "_sorted")
	orig := filepath.Join(src, "IMG.jpg")
	destCopy := filepath.Join(dst, "fam", "2025", "2025-01-01", "IMG.jpg")
	video := filepath.Join(src, "CLIP.mov")
	writeCleanFile(t, orig, []byte("PIC"))
	writeCleanFile(t, destCopy, []byte("PIC"))
	writeCleanFile(t, video, []byte("VIDEO"))

	cfg := config.CleanConfig{Source: src, DestRoot: dst, Delete: true}
	sum, err := app.Clean(context.Background(), cfg, discardLogger())
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if sum.Deleted != 1 {
		t.Errorf("Deleted = %d, want 1", sum.Deleted)
	}
	if _, e := os.Lstat(orig); e == nil {
		t.Error("copied original should be deleted")
	}
	if _, e := os.Lstat(video); e != nil {
		t.Error("uncopied file should remain")
	}
	if _, e := os.Lstat(destCopy); e != nil {
		t.Error("destination must be untouched")
	}
}

// TestCleanPreviewThenCommit covers US2/SC-006: a dry-run preview deletes nothing,
// then a delete run removes exactly the previewed set.
func TestCleanPreviewThenCommit(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(src, "_sorted")
	orig := filepath.Join(src, "IMG.jpg")
	writeCleanFile(t, orig, []byte("PIC"))
	writeCleanFile(t, filepath.Join(dst, "IMG.jpg"), []byte("PIC"))

	preview, err := app.Clean(context.Background(),
		config.CleanConfig{Source: src, DestRoot: dst, Delete: false}, discardLogger())
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.WouldDelete != 1 || preview.Deleted != 0 {
		t.Errorf("preview = %+v, want WouldDelete=1 Deleted=0", preview)
	}
	if _, e := os.Lstat(orig); e != nil {
		t.Error("preview must not delete anything")
	}

	commit, err := app.Clean(context.Background(),
		config.CleanConfig{Source: src, DestRoot: dst, Delete: true}, discardLogger())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if commit.Deleted != preview.WouldDelete {
		t.Errorf("committed deletes (%d) != previewed plan (%d)", commit.Deleted, preview.WouldDelete)
	}
	if _, e := os.Lstat(orig); e == nil {
		t.Error("commit must delete the copied original")
	}
}
