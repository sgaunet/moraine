package cli_test

import (
	"bytes"
	"image"
	"image/png"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/sgaunet/moraine/internal/cli"
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
