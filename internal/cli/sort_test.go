package cli_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sgaunet/moraine/internal/cli"
	"github.com/sgaunet/moraine/internal/exiftooltest"
)

func TestSortOrganizesPNG(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	writePNG(t, filepath.Join(src, "a.png"))

	// A working stub satisfies the unconditional exiftool check even with the model off.
	exifPath, err := exiftooltest.Stub(t.TempDir(), exiftooltest.Options{})
	if err != nil {
		t.Fatal(err)
	}

	code := cli.Execute("dev", []string{
		"sort", "--exiftool", exifPath, "--sample", "0", "--dest", dest, src,
	}, io.Discard, io.Discard)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}

	// The PNG should have been organized somewhere under dest (behavior parity).
	var copied bool
	_ = filepath.Walk(dest, func(_ string, info os.FileInfo, _ error) error {
		if info != nil && !info.IsDir() && strings.HasSuffix(info.Name(), ".png") {
			copied = true
		}
		return nil
	})
	if !copied {
		t.Error("expected the PNG to be organized under dest")
	}
}

func TestSortShortFlags(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	writePNG(t, filepath.Join(src, "a.png"))
	exifPath, err := exiftooltest.Stub(t.TempDir(), exiftooltest.Options{})
	if err != nil {
		t.Fatal(err)
	}
	// -s (sample) and -d (dest) shorthands must work like their long forms.
	code := cli.Execute("dev", []string{"sort", "--exiftool", exifPath, "-s", "0", "-d", dest, src}, io.Discard, io.Discard)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
}

func TestSortMissingExiftoolFailsFast(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	writePNG(t, filepath.Join(src, "a.png"))

	var stderr bytes.Buffer
	code := cli.Execute("dev", []string{
		"sort", "--exiftool", filepath.Join(t.TempDir(), "no-such-exiftool"),
		"--sample", "0", "--dest", dest, src,
	}, io.Discard, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (runtime); stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "exiftool") {
		t.Errorf("stderr should mention exiftool; got: %s", stderr.String())
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

func TestSortMissingSourceIsRuntime(t *testing.T) {
	// Validate runs before the exiftool preflight, so a missing source is exit 1.
	code := cli.Execute("dev", []string{
		"sort", "--sample", "0", "--dest", t.TempDir(), filepath.Join(t.TempDir(), "nope"),
	}, io.Discard, io.Discard)
	if code != 1 {
		t.Fatalf("missing source exit = %d, want 1 (runtime)", code)
	}
}

func TestSortInvalidValuesAreUsage(t *testing.T) {
	tmp := t.TempDir()
	tests := []struct {
		name string
		args []string
	}{
		{"bad gap", []string{"sort", "--gap", "nope", tmp}},
		{"bad themes slug", []string{"sort", "--themes", "Bad Theme", tmp}},
		{"negative sample", []string{"sort", "--sample", "-1", tmp}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code := cli.Execute("dev", tc.args, io.Discard, io.Discard)
			if code != 2 {
				t.Errorf("%s: exit = %d, want 2 (usage)", tc.name, code)
			}
		})
	}
}
