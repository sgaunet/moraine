// Package exiftooltest builds a fake `exiftool` executable for tests, so the
// exec-based RAW preview path can be exercised without a real exiftool install.
// It is the exec analog of net/http/httptest used by the HTTP-facing tests.
package exiftooltest

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Options configures the stub's behavior.
type Options struct {
	// Version is printed in response to `-ver` (default "13.55").
	Version string
	// VerFails makes `-ver` exit non-zero, simulating a broken/unusable binary.
	VerFails bool
	// Previews maps an exiftool tag (JpgFromRaw|PreviewImage|ThumbnailImage) to
	// the bytes emitted for `-b -<tag>`. A tag with no entry yields empty output.
	Previews map[string][]byte
	// SleepMS delays the extraction response (not `-ver`), for timeout tests.
	SleepMS int
}

// Stub writes a fake exiftool executable into dir and returns its absolute path.
// Point Config.ExifToolPath (or an Extractor.Path) at it.
func Stub(dir string, opts Options) (string, error) {
	if runtime.GOOS == "windows" {
		return "", fmt.Errorf("exiftooltest stub is not supported on windows")
	}
	version := opts.Version
	if version == "" {
		version = "13.55"
	}
	for tag, data := range opts.Previews {
		if err := os.WriteFile(filepath.Join(dir, tag+".bin"), data, 0o600); err != nil {
			return "", fmt.Errorf("writing preview payload %q: %w", tag, err)
		}
	}

	verCmd := "printf '%s\\n' " + shellQuote(version)
	if opts.VerFails {
		verCmd = "exit 1"
	}
	sleep := ""
	if opts.SleepMS > 0 {
		sleep = fmt.Sprintf("sleep %d.%03d\n", opts.SleepMS/1000, opts.SleepMS%1000)
	}

	script := "#!/bin/sh\n" +
		"DIR=" + shellQuote(dir) + "\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = \"-ver\" ]; then " + verCmd + "; exit 0; fi\n" +
		"done\n" +
		sleep +
		"tag=\"\"\n" +
		"for a in \"$@\"; do\n" +
		"  case \"$a\" in\n" +
		"    -JpgFromRaw) tag=JpgFromRaw ;;\n" +
		"    -PreviewImage) tag=PreviewImage ;;\n" +
		"    -ThumbnailImage) tag=ThumbnailImage ;;\n" +
		"  esac\n" +
		"done\n" +
		"if [ -n \"$tag\" ] && [ -f \"$DIR/$tag.bin\" ]; then cat \"$DIR/$tag.bin\"; fi\n" +
		"exit 0\n"

	path := filepath.Join(dir, "exiftool")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil { //nolint:gosec // test stub must be executable
		return "", fmt.Errorf("writing exiftool stub: %w", err)
	}
	return path, nil
}

// shellQuote single-quotes s for safe embedding in the /bin/sh stub.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
