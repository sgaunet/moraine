package cli_test

import (
	"bytes"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sgaunet/moraine/internal/cli"
	"github.com/sgaunet/moraine/internal/exiftooltest"
)

// writePNG writes a tiny valid PNG at path (shared helper for the sort tests).
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

func TestExecuteHelp(t *testing.T) {
	var out bytes.Buffer
	code := cli.Execute("dev", []string{"--help"}, &out, io.Discard)
	if code != 0 {
		t.Fatalf("--help exit = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "automatic photo organizer") {
		t.Errorf("help missing overview; got: %s", out.String())
	}
}

func TestExecuteBareShowsHelp(t *testing.T) {
	var out bytes.Buffer
	code := cli.Execute("dev", []string{}, &out, io.Discard)
	if code != 0 {
		t.Fatalf("bare exit = %d, want 0", code)
	}
	for _, w := range []string{"sort", "clean", "version"} {
		if !strings.Contains(out.String(), w) {
			t.Errorf("bare help missing subcommand %q; got: %s", w, out.String())
		}
	}
}

// writeCLIFile writes a non-image companion file.
func writeCLIFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// stubExif returns a fake exiftool binary path satisfying rawpreview.EnsureAvailable.
func stubExif(t *testing.T) string {
	t.Helper()
	p, err := exiftooltest.Stub(t.TempDir(), exiftooltest.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// collectNames returns the set of regular-file base names anywhere under root.
func collectNames(t *testing.T, root string) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	err := filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			names[d.Name()] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return names
}

// TestSortCompanionsDefaultAndOptOut covers US3: companions are copied by default,
// --sidecars=false copies photos only (SC-005), and a bad bool value is a usage
// error (exit 2).
func TestSortCompanionsDefaultAndOptOut(t *testing.T) {
	mk := func(t *testing.T) (src, dest, exif string) {
		t.Helper()
		src, dest = t.TempDir(), t.TempDir()
		writePNG(t, filepath.Join(src, "a.png"))
		writeCLIFile(t, filepath.Join(src, "a.png.xmp"), "appended")
		writeCLIFile(t, filepath.Join(src, "a.xmp"), "base")
		return src, dest, stubExif(t)
	}

	t.Run("default copies companions", func(t *testing.T) {
		src, dest, exif := mk(t)
		var out, errb bytes.Buffer
		code := cli.Execute("dev",
			[]string{"sort", "--sample", "0", "--exiftool", exif, "--dest", dest, src}, &out, &errb)
		if code != 0 {
			t.Fatalf("exit = %d; stderr=%s", code, errb.String())
		}
		got := collectNames(t, dest)
		for _, n := range []string{"a.png", "a.png.xmp", "a.xmp"} {
			if !got[n] {
				t.Errorf("default sort missing %q in dest; got %v", n, got)
			}
		}
	})

	t.Run("--sidecars=false copies photos only", func(t *testing.T) {
		src, dest, exif := mk(t)
		var out, errb bytes.Buffer
		code := cli.Execute("dev",
			[]string{"sort", "--sidecars=false", "--sample", "0", "--exiftool", exif, "--dest", dest, src}, &out, &errb)
		if code != 0 {
			t.Fatalf("exit = %d; stderr=%s", code, errb.String())
		}
		got := collectNames(t, dest)
		if !got["a.png"] {
			t.Error("photo must still be copied")
		}
		if got["a.png.xmp"] || got["a.xmp"] {
			t.Errorf("--sidecars=false must copy no companions; got %v", got)
		}
	})

	t.Run("invalid bool is a usage error", func(t *testing.T) {
		src, dest, exif := mk(t)
		var out, errb bytes.Buffer
		code := cli.Execute("dev",
			[]string{"sort", "--sidecars=notabool", "--sample", "0", "--exiftool", exif, "--dest", dest, src}, &out, &errb)
		if code != 2 {
			t.Fatalf("exit = %d, want 2 (usage); stderr=%s", code, errb.String())
		}
	})
}

// TestSortReportsCompanionCounts covers FR-010/SC-007: companion outcomes are
// visible in the run output (a per-companion line and the summary counters).
func TestSortReportsCompanionCounts(t *testing.T) {
	src, dest := t.TempDir(), t.TempDir()
	writePNG(t, filepath.Join(src, "a.png"))
	writeCLIFile(t, filepath.Join(src, "a.xmp"), "base")
	exif := stubExif(t)

	var out, errb bytes.Buffer
	code := cli.Execute("dev",
		[]string{"sort", "--sample", "0", "--exiftool", exif, "--dest", dest, src}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%s", code, errb.String())
	}
	s := out.String()
	for _, want := range []string{"msg=companion", "companions_copied=1"} {
		if !strings.Contains(s, want) {
			t.Errorf("sort output missing %q\n---\n%s", want, s)
		}
	}
}
