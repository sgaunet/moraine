// Package rawpreview is the single place that talks to the external exiftool
// program. It verifies exiftool is available (mandatory at startup) and extracts
// the embedded JPEG preview from a file whose pixels Go cannot decode — a camera
// RAW, or a HEIC — so the vision model can "see" it. Previews are captured in
// memory (exiftool's stdout) and never written to disk (feature 003, FR-005). It
// depends on no transport or storage package.
package rawpreview

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrNoPreview means the file exposes no embedded image under any supported tag.
// Callers treat it as "skip this photo as model input" (non-fatal), not as an
// operational failure. It is the normal outcome for a HEIC written without a
// thumbnail, and for a RAW whose maker stores no JPEG alongside the sensor data.
var ErrNoPreview = errors.New("no embedded preview")

// previewTags are preferred largest-first: a full-size embedded JPEG before the
// smaller preview, before the thumbnail of last resort (FR-004). The list spans
// both sources: JpgFromRaw is a RAW tag, while a HEIC typically answers only to
// ThumbnailImage. A tag the file does not carry is simply absent from the report
// and the next one is taken.
//
// The order is used twice — to build the argument vector and to pick the winner out
// of the reply — so the priority cannot drift between asking and choosing.
var previewTags = []string{"JpgFromRaw", "PreviewImage", "ThumbnailImage"}

// b64Prefix is how `exiftool -json -b` marks a binary tag value: the JSON string
// carries the bytes base64-encoded behind this marker. A value without it is text,
// not an image, and is not a preview.
const b64Prefix = "base64:"

// Extractor extracts embedded previews via exiftool. The zero value is unusable;
// build one with NewExtractor.
type Extractor struct {
	Path    string        // exiftool executable (name on PATH or absolute path)
	Timeout time.Duration // per-invocation bound; <= 0 means no extra bound beyond ctx
	Logger  *slog.Logger
}

// NewExtractor builds an Extractor for the given exiftool path and per-call timeout.
func NewExtractor(path string, timeout time.Duration) *Extractor {
	if path == "" {
		path = "exiftool"
	}
	return &Extractor{Path: path, Timeout: timeout, Logger: slog.Default()}
}

// Extract returns the largest available embedded JPEG preview of path (a RAW or a
// HEIC), in memory. It returns ErrNoPreview when the file carries none under any
// supported tag, or a wrapped error when exiftool cannot run or times out. No
// temporary file is written.
//
// Every tag is requested in a single `-json -b` invocation and the best one is
// chosen from the reply. Asking for them one at a time — which is what this used to
// do — cost up to three exiftool processes per photo, and process startup dominates
// the per-RAW cost. It also made Timeout mean "per tag" rather than "per photo", so
// a three-tag probe could take three times the bound and overrun the caller's own
// budget. Base64 inflates the transfer by a third, which is a good trade against two
// Perl interpreter startups.
func (e *Extractor) Extract(ctx context.Context, rawPath string) ([]byte, error) {
	args := make([]string, 0, len(previewTags)+4)
	args = append(args, "-json", "-b")
	for _, tag := range previewTags {
		args = append(args, "-"+tag)
	}
	// "--" is exiftool's documented end-of-options marker and operand() makes the
	// path unmistakable on its own; both are here because exiftool carries
	// write-capable options (-@, -o, -tagsFromFile) and this is the only argv the
	// tool ever hands it. rawPath itself, not the operand form, stays in the
	// messages below, so logs name the file the caller asked about.
	args = append(args, "--", operand(rawPath))

	out, err := e.run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("extracting preview from %q: %w", rawPath, err)
	}
	data, tag, err := pickPreview(out)
	if err != nil {
		return nil, fmt.Errorf("extracting preview from %q: %w", rawPath, err)
	}
	if len(data) == 0 {
		return nil, ErrNoPreview
	}
	e.log().Debug("extracted RAW preview", "file", rawPath, "tag", tag, "bytes", len(data))
	return data, nil
}

// pickPreview reads exiftool's `-json -b` report and returns the bytes of the
// highest-priority tag it carries, with the tag that supplied them.
//
// Empty or unparseable output is "no preview", not a failure: exiftool writes its
// diagnostics to stderr and leaves stdout empty for a file it cannot read, which is
// exactly the outcome a caller treats as "skip this photo as model input". Only a
// value that claims to be base64 and is not is an error, because that means the
// report was understood and still could not be used.
func pickPreview(stdout []byte) ([]byte, string, error) {
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 {
		return nil, "", nil
	}
	var report []map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &report); err != nil || len(report) == 0 {
		return nil, "", nil
	}
	for _, tag := range previewTags {
		raw, ok := report[0][tag]
		if !ok {
			continue
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			continue // not a string, so not a binary tag value
		}
		encoded, ok := strings.CutPrefix(value, b64Prefix)
		if !ok {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, "", fmt.Errorf("decoding the %s preview: %w", tag, err)
		}
		if len(data) > 0 {
			return data, tag, nil
		}
	}
	return nil, "", nil
}

func (e *Extractor) log() *slog.Logger {
	if e.Logger != nil {
		return e.Logger
	}
	return slog.Default()
}

// run executes exiftool with the given args (no shell), bounded by e.Timeout,
// and returns its stdout. A file exiftool will not read yields empty stdout (and
// possibly a non-zero exit), which is reported as empty bytes — not an error; only a
// failure to start the process or a timeout is a hard error. Because Extract now
// makes a single call, e.Timeout bounds the whole extraction rather than one tag.
// operand returns path in the one form no option parser can mistake for a flag.
// An explicit "./" is the only neutraliser every external tool this repo shells
// out to accepts: exiftool and heif-convert honour a "--" terminator, sips rejects
// "--" outright and ffmpeg misreads the "-i" that would follow it. filepath.Join
// cannot be used — it cleans the "./" straight back off.
//
// Today every path reaching here is absolute, because config.Config.Validate runs
// filepath.Abs on the source root. This does not depend on that: the guard belongs
// next to the exec call, not several packages away.
func operand(path string) string {
	if strings.HasPrefix(path, "-") {
		return "." + string(filepath.Separator) + path
	}
	return path
}

func (e *Extractor) run(ctx context.Context, args ...string) ([]byte, error) {
	cctx := ctx
	if e.Timeout > 0 {
		var cancel context.CancelFunc
		cctx, cancel = context.WithTimeout(ctx, e.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(cctx, e.Path, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if cctx.Err() != nil {
		return nil, fmt.Errorf("exiftool timed out: %w", cctx.Err())
	}
	if err != nil {
		if _, ok := errors.AsType[*exec.ExitError](err); ok {
			// exiftool ran but reported a problem for this tag (e.g. tag absent):
			// treat whatever it produced as the result for this tag.
			return stdout.Bytes(), nil
		}
		return nil, fmt.Errorf("running exiftool %q: %w (stderr: %s)",
			e.Path, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}
