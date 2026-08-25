package heicpreview_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/heicpreview"
)

// stubTool writes an executable named after a real converter into dir and puts
// dir first on PATH, so Detect finds it. The script echoes payload on stdout for
// the stdout tools and writes it to the --out/last argument for the others,
// mimicking each tool's actual output contract.
func stubTool(t *testing.T, name, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are not supported on windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o700); err != nil { //nolint:gosec // a test stub must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func heicFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "IMG_0001.heic")
	if err := os.WriteFile(p, []byte("not really heic"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDetectFindsNothingOnAnEmptyPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if got := heicpreview.Detect(time.Second); got != nil {
		t.Fatalf("Detect found %q on an empty PATH; want nil", got.Name())
	}
}

func TestExtractFromAStdoutConverter(t *testing.T) {
	// ffmpeg and magick write the JPEG to stdout; nothing touches the disk.
	stubTool(t, "ffmpeg", `printf 'JPEGBYTES'`)

	conv := heicpreview.Detect(5 * time.Second)
	if conv == nil {
		t.Fatal("Detect returned nil; want the stubbed ffmpeg")
	}
	if conv.Name() != "ffmpeg" {
		t.Fatalf("Name = %q; want ffmpeg", conv.Name())
	}
	got, err := conv.Extract(context.Background(), heicFile(t))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if string(got) != "JPEGBYTES" {
		t.Errorf("Extract = %q; want the converter's stdout", got)
	}
}

func TestExtractFromAFileWritingConverter(t *testing.T) {
	// sips cannot write to stdout, so it is handed a scratch path to fill.
	stubTool(t, "sips", `
out=""
while [ $# -gt 0 ]; do
  if [ "$1" = "--out" ]; then out="$2"; fi
  shift
done
printf 'FROMFILE' > "$out"
`)

	conv := heicpreview.Detect(5 * time.Second)
	if conv == nil {
		t.Fatal("Detect returned nil; want the stubbed sips")
	}
	got, err := conv.Extract(context.Background(), heicFile(t))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if string(got) != "FROMFILE" {
		t.Errorf("Extract = %q; want what the converter wrote", got)
	}
}

func TestExtractRemovesItsScratchFile(t *testing.T) {
	// The scratch file is an implementation detail and must not outlive the call.
	stubTool(t, "sips", `
out=""
while [ $# -gt 0 ]; do
  if [ "$1" = "--out" ]; then out="$2"; fi
  shift
done
printf '%s' "$out" > "$out"
`)

	conv := heicpreview.Detect(5 * time.Second)
	if conv == nil {
		t.Fatal("Detect returned nil")
	}
	got, err := conv.Extract(context.Background(), heicFile(t))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if _, err := os.Stat(string(got)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("scratch file %q still exists after Extract (stat err = %v)", got, err)
	}
}

func TestExtractReportsAFailingConverter(t *testing.T) {
	stubTool(t, "ffmpeg", `echo "unsupported codec" >&2; exit 1`)

	conv := heicpreview.Detect(5 * time.Second)
	if conv == nil {
		t.Fatal("Detect returned nil")
	}
	_, err := conv.Extract(context.Background(), heicFile(t))
	if err == nil {
		t.Fatal("expected an error when the converter exits non-zero")
	}
	if !strings.Contains(err.Error(), "unsupported codec") {
		t.Errorf("error %q does not carry the converter's stderr", err)
	}
}

func TestExtractReportsAnEmptyResult(t *testing.T) {
	// Exit 0 with no output is a failure too: there is no image to send.
	stubTool(t, "ffmpeg", `exit 0`)

	conv := heicpreview.Detect(5 * time.Second)
	if conv == nil {
		t.Fatal("Detect returned nil")
	}
	if _, err := conv.Extract(context.Background(), heicFile(t)); err == nil {
		t.Fatal("expected an error when the converter produces nothing")
	}
}

func TestExtractHonoursTheTimeout(t *testing.T) {
	stubTool(t, "ffmpeg", `sleep 5`)

	conv := heicpreview.Detect(50 * time.Millisecond)
	if conv == nil {
		t.Fatal("Detect returned nil")
	}
	start := time.Now()
	if _, err := conv.Extract(context.Background(), heicFile(t)); err == nil {
		t.Fatal("expected a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Extract took %s; the timeout should have cut it short", elapsed)
	}
}

func TestNilConverterIsUsableAndReportsNoTool(t *testing.T) {
	// Detect returns a nil *Converter when nothing is installed; the methods must
	// not panic on it, since a caller may log Name() before checking.
	var conv *heicpreview.Converter
	if conv.Name() != "" {
		t.Errorf("Name on a nil converter = %q; want empty", conv.Name())
	}
	if _, err := conv.Extract(context.Background(), "x.heic"); err == nil {
		t.Error("Extract on a nil converter must report that none is available")
	}
}

func TestInstallableNamesTheCandidates(t *testing.T) {
	got := heicpreview.Installable()
	if len(got) == 0 {
		t.Fatal("Installable returned nothing; the log line would name no remedy")
	}
	for _, want := range []string{"sips", "heif-convert", "ffmpeg", "magick"} {
		if !slicesContains(got, want) {
			t.Errorf("Installable %v does not mention %q", got, want)
		}
	}
}

func slicesContains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// argvScript builds a converter stub that records the argument vector it was
// handed, one argument per line, into log. It then produces its JPEG the way the
// real tool would: on stdout, or in the file it was told to write — the argument
// after --out for sips, the last one for heif-convert.
func argvScript(log string, stdout bool) string {
	record := `printf '%s\n' "$@" > ` + log + "\n"
	if stdout {
		return record + `printf 'JPEGBYTES'`
	}
	return record + `
out=""
while [ $# -gt 0 ]; do
  if [ "$1" = "--out" ]; then out="$2"; fi
  last="$1"
  shift
done
[ -n "$out" ] || out="$last"
printf 'JPEGBYTES' > "$out"
`
}

func recordedArgs(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("reading the stub's argument log: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// TestExtractPassesTheSourceAsALiteralOperand is the regression test for issue
// #35's argv item: the HEIC path used to be a bare positional argument, so a name
// starting with '-' would have been read as an option. Every converter is covered
// because each takes the source in a different position, and a "--" terminator is
// not an option here — sips rejects it and ffmpeg misreads the "-i" that follows.
func TestExtractPassesTheSourceAsALiteralOperand(t *testing.T) {
	const dashed = "-o.heic"
	tests := []struct {
		tool   string
		stdout bool
	}{
		{"sips", false},
		{"heif-convert", false},
		{"ffmpeg", true},
		{"magick", true},
	}
	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "argv")
			stubTool(t, tt.tool, argvScript(log, tt.stdout))

			conv := heicpreview.Detect(5 * time.Second)
			if conv == nil {
				t.Fatalf("Detect returned nil; want the stubbed %s", tt.tool)
			}
			if _, err := conv.Extract(context.Background(), dashed); err != nil {
				t.Fatalf("Extract: %v", err)
			}

			args := recordedArgs(t, log)
			if !slicesContains(args, "./"+dashed) {
				t.Errorf("%s argv %q carries no neutralised source; want %q", tt.tool, args, "./"+dashed)
			}
			if slicesContains(args, dashed) {
				t.Errorf("%s argv %q hands over the bare %q, which it would parse as an option",
					tt.tool, args, dashed)
			}
		})
	}
}

// TestExtractLeavesAnOrdinaryPathAlone pins the other half of the rule: only a
// name that could be read as an option is rewritten, so the paths a real run
// produces reach the converter — and its error messages — unchanged.
func TestExtractLeavesAnOrdinaryPathAlone(t *testing.T) {
	log := filepath.Join(t.TempDir(), "argv")
	stubTool(t, "ffmpeg", argvScript(log, true))

	conv := heicpreview.Detect(5 * time.Second)
	if conv == nil {
		t.Fatal("Detect returned nil; want the stubbed ffmpeg")
	}
	src := heicFile(t)
	if _, err := conv.Extract(context.Background(), src); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	args := recordedArgs(t, log)
	if !slicesContains(args, src) {
		t.Errorf("ffmpeg argv %q does not carry %q verbatim", args, src)
	}
}
