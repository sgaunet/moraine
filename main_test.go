package main

import (
	"bytes"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sgaunet/moraine/internal/exiftooltest"
)

func writePNG(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
}

func TestRunMissingExiftoolFailsFast(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	writePNG(t, filepath.Join(src, "a.png"))

	var stderr bytes.Buffer
	code := run([]string{
		"-exiftool", filepath.Join(t.TempDir(), "no-such-exiftool"),
		"-sample", "0", "-dest", dest, src,
	}, io.Discard, &stderr)

	if code != exitRuntime {
		t.Fatalf("exit code = %d; want %d (runtime error)", code, exitRuntime)
	}
	msg := stderr.String()
	for _, want := range []string{"exiftool", "-exiftool"} {
		if !strings.Contains(msg, want) {
			t.Errorf("stderr missing %q; got: %s", want, msg)
		}
	}
	// Nothing must have been scanned or copied.
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("destination not empty after fail-fast: %v", entries)
	}
}

func TestRunWithExiftoolProceeds(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	writePNG(t, filepath.Join(src, "a.png"))

	// A working stub satisfies the unconditional check even with the model off.
	exifPath, err := exiftooltest.Stub(t.TempDir(), exiftooltest.Options{})
	if err != nil {
		t.Fatal(err)
	}

	code := run([]string{
		"-exiftool", exifPath, "-sample", "0", "-dest", dest, src,
	}, io.Discard, io.Discard)
	if code != exitOK {
		t.Fatalf("exit code = %d; want %d", code, exitOK)
	}
	// The PNG should have been organized somewhere under dest.
	var copied bool
	_ = filepath.Walk(dest, func(_ string, info os.FileInfo, _ error) error {
		if info != nil && !info.IsDir() && strings.HasSuffix(info.Name(), ".png") {
			copied = true
		}
		return nil
	})
	if !copied {
		t.Error("expected the PNG to be organized under dest when exiftool is available")
	}
}

func TestRunCleanDispatchDeletes(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(src, "_sorted")
	orig := filepath.Join(src, "a.jpg")
	if err := os.MkdirAll(filepath.Join(dst, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orig, []byte("PIC"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "x", "a.jpg"), []byte("PIC"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	code := run([]string{"clean", "-delete", "-dest", dst, src}, io.Discard, &stderr)
	if code != exitOK {
		t.Fatalf("exit = %d; stderr: %s", code, stderr.String())
	}
	if _, err := os.Lstat(orig); err == nil {
		t.Error("`clean -delete` should remove the copied original")
	}
}

func TestRunCleanHelp(t *testing.T) {
	var stdout bytes.Buffer
	code := run([]string{"clean", "-help"}, &stdout, io.Discard)
	if code != exitOK {
		t.Errorf("clean -help exit = %d, want %d", code, exitOK)
	}
	if !strings.Contains(stdout.String(), "moraine clean —") {
		t.Errorf("clean help banner missing; got: %s", stdout.String())
	}
}

func TestRunCleanUsageError(t *testing.T) {
	code := run([]string{"clean"}, io.Discard, io.Discard) // missing source
	if code != exitUsage {
		t.Errorf("missing source exit = %d, want %d (usage)", code, exitUsage)
	}
}

func TestRunCleanMissingDest(t *testing.T) {
	src := t.TempDir()
	code := run([]string{"clean", "-dest", filepath.Join(src, "nope"), src}, io.Discard, io.Discard)
	if code != exitRuntime {
		t.Errorf("missing dest exit = %d, want %d (runtime)", code, exitRuntime)
	}
}

func TestRunSortHelpStillWorks(t *testing.T) {
	var stdout bytes.Buffer
	code := run([]string{"-help"}, &stdout, io.Discard)
	if code != exitOK {
		t.Errorf("sort -help exit = %d, want %d", code, exitOK)
	}
	out := stdout.String()
	if strings.Contains(out, "moraine clean —") {
		t.Error("-help should show the sort usage, not the clean banner")
	}
	if !strings.Contains(out, "automatic photo organizer") {
		t.Errorf("sort usage banner missing; got: %s", out)
	}
}
