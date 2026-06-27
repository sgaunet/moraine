package cli_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/sgaunet/moraine/internal/cli"
)

// stageCopiedOriginal creates a source tree whose <src>/a.jpg has a byte-identical
// copy under <src>/_sorted/x/a.jpg, returning the source dir and the original path.
func stageCopiedOriginal(t *testing.T) (src, orig string) {
	t.Helper()
	src = t.TempDir()
	dst := filepath.Join(src, "_sorted", "x")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	orig = filepath.Join(src, "a.jpg")
	if err := os.WriteFile(orig, []byte("PIC"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "a.jpg"), []byte("PIC"), 0o644); err != nil {
		t.Fatal(err)
	}
	return src, orig
}

func TestCleanDeleteRemovesCopiedOriginal(t *testing.T) {
	src, orig := stageCopiedOriginal(t)
	dst := filepath.Join(src, "_sorted")

	code := cli.Execute("dev", []string{"clean", "--delete", "--dest", dst, src}, io.Discard, io.Discard)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if _, err := os.Lstat(orig); err == nil {
		t.Error("`clean --delete` should remove the copied original")
	}
}

func TestCleanDryRunKeepsOriginal(t *testing.T) {
	src, orig := stageCopiedOriginal(t)
	dst := filepath.Join(src, "_sorted")

	code := cli.Execute("dev", []string{"clean", "--dest", dst, src}, io.Discard, io.Discard)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if _, err := os.Lstat(orig); err != nil {
		t.Error("default (dry-run) clean must not delete anything")
	}
}

func TestCleanMissingDestIsRuntime(t *testing.T) {
	src := t.TempDir()
	code := cli.Execute("dev", []string{"clean", "--dest", filepath.Join(src, "nope"), src}, io.Discard, io.Discard)
	if code != 1 {
		t.Fatalf("missing dest exit = %d, want 1 (runtime)", code)
	}
}

func TestCleanMissingArgIsUsage(t *testing.T) {
	code := cli.Execute("dev", []string{"clean"}, io.Discard, io.Discard)
	if code != 2 {
		t.Fatalf("missing source exit = %d, want 2 (usage)", code)
	}
}
