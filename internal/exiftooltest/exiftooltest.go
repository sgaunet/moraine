// Package exiftooltest builds a fake `exiftool` executable for tests, so the
// exec-based RAW preview path can be exercised without a real exiftool install.
// It is the exec analog of net/http/httptest used by the HTTP-facing tests.
package exiftooltest

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// invocationsFile is where the stub records one line per extraction call, so a test
// can assert how many exiftool processes an Extract actually cost.
const invocationsFile = "invocations"

// argvFile is where the stub records the argument vector it was handed, one
// argument per line, so a test can assert how a path reached exiftool. It is
// rewritten by each extraction rather than appended to, so it always holds the
// most recent call.
const argvFile = "argv"

// Options configures the stub's behavior.
type Options struct {
	// Version is printed in response to `-ver` (default "13.55").
	Version string
	// VerFails makes `-ver` exit non-zero, simulating a broken/unusable binary.
	VerFails bool
	// Previews maps an exiftool tag (JpgFromRaw|PreviewImage|ThumbnailImage) to
	// the bytes reported for that tag. A tag with no entry is absent from the
	// report, exactly as real exiftool omits a tag the file does not carry.
	Previews map[string][]byte
	// SleepMS delays the extraction response (not `-ver`), for timeout tests.
	SleepMS int
}

// knownTags are the tags the stub answers for, in the order it emits them. It
// mirrors rawpreview's own list; the stub reports every requested tag it has and
// leaves the priority choice to the caller, as real exiftool does.
var knownTags = []string{"JpgFromRaw", "PreviewImage", "ThumbnailImage"}

// Stub writes a fake exiftool executable into dir and returns its absolute path.
// Point Config.ExifToolPath (or an Extractor.Path) at it.
//
// It answers `-ver`, and otherwise emits an exiftool `-json -b` report: one JSON
// object per file, carrying a "base64:"-prefixed value for each requested tag that
// Options.Previews defines. Tags with no entry are omitted rather than reported
// empty, which is what real exiftool does and what makes "no preview" detectable.
func Stub(dir string, opts Options) (string, error) {
	if runtime.GOOS == "windows" {
		return "", errors.New("exiftooltest stub is not supported on windows")
	}
	version := opts.Version
	if version == "" {
		version = "13.55"
	}
	// Encode here rather than in the shell: base64(1) is not portable enough to
	// rely on inside the stub, and the payload is known at build time anyway.
	for tag, data := range opts.Previews {
		encoded := base64.StdEncoding.EncodeToString(data)
		if err := os.WriteFile(filepath.Join(dir, tag+".b64"), []byte(encoded), 0o600); err != nil {
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
		"printf 'x\\n' >> \"$DIR/" + invocationsFile + "\"\n" +
		"printf '%s\\n' \"$@\" > \"$DIR/" + argvFile + "\"\n" +
		sleep +
		"printf '[{\\n  \"SourceFile\": \"stub\"'\n" +
		"for a in \"$@\"; do\n" +
		"  case \"$a\" in\n" +
		"    " + strings.Join(dashed(knownTags), "|") + ")\n" +
		"      tag=\"${a#-}\"\n" +
		"      if [ -f \"$DIR/$tag.b64\" ]; then\n" +
		"        printf ',\\n  \"%s\": \"base64:%s\"' \"$tag\" \"$(cat \"$DIR/$tag.b64\")\"\n" +
		"      fi\n" +
		"      ;;\n" +
		"  esac\n" +
		"done\n" +
		"printf '\\n}]\\n'\n" +
		"exit 0\n"

	path := filepath.Join(dir, "exiftool")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil { //nolint:gosec // test stub must be executable
		return "", fmt.Errorf("writing exiftool stub: %w", err)
	}
	return path, nil
}

// Invocations reports how many times the stub in dir was run for an extraction.
// `-ver` probes are not counted: they exit before the tally, so a caller measuring
// the cost of Extract sees only the extraction processes.
func Invocations(dir string) (int, error) {
	data, err := os.ReadFile(filepath.Join(dir, invocationsFile))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading the stub's invocation log: %w", err)
	}
	return strings.Count(string(data), "\n"), nil
}

// Args reports the argument vector the stub in dir was handed by the most recent
// extraction, one element per entry. `-ver` probes are not recorded: they exit
// before the tally, so a caller measuring how Extract builds its command line sees
// only the extraction. An argument containing a newline would be split across
// entries; none of the callers passes one.
func Args(dir string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(dir, argvFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the stub's argument log: %w", err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n"), nil
}

// dashed prefixes each tag with the '-' that names it on an exiftool command line.
func dashed(tags []string) []string {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		out = append(out, "-"+t)
	}
	return out
}

// shellQuote single-quotes s for safe embedding in the /bin/sh stub.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
