package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMoveFileRenamePath(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.jpg")
	dst := filepath.Join(dir, "dst.jpg")
	must(t, os.WriteFile(src, []byte("payload"), 0o600))

	if err := moveFile(src, dst); err != nil {
		t.Fatalf("moveFile: %v", err)
	}
	if exists(src) {
		t.Error("source should be gone after move")
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "payload" {
		t.Fatalf("dst content = %q, err = %v; want \"payload\"", got, err)
	}
}

func TestMoveFileMissingSource(t *testing.T) {
	dir := t.TempDir()
	err := moveFile(filepath.Join(dir, "nope.jpg"), filepath.Join(dir, "dst.jpg"))
	if err == nil {
		t.Fatal("expected error moving a missing source")
	}
}

func TestCopyThenRemove(t *testing.T) {
	// Exercises the cross-device fallback path directly (copy + fsync + remove).
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "sub", "dst.bin")
	must(t, os.MkdirAll(filepath.Dir(dst), 0o755))
	payload := []byte("durable-bytes-1234567890")
	must(t, os.WriteFile(src, payload, 0o600))

	if err := copyThenRemove(src, dst); err != nil {
		t.Fatalf("copyThenRemove: %v", err)
	}
	if exists(src) {
		t.Error("source should be removed after copyThenRemove")
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("dst content = %q, err = %v", got, err)
	}
}

func TestCopyThenRemoveRefusesExistingDest(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")
	must(t, os.WriteFile(src, []byte("new"), 0o600))
	must(t, os.WriteFile(dst, []byte("existing"), 0o600))

	// O_EXCL must prevent overwriting an existing destination file.
	if err := copyThenRemove(src, dst); err == nil {
		t.Fatal("copyThenRemove should refuse to overwrite an existing dst")
	}
	if got, _ := os.ReadFile(dst); string(got) != "existing" {
		t.Errorf("dst was overwritten: %q", got)
	}
}
