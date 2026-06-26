// Package rawpreview is the single place that talks to the external exiftool
// program. It verifies exiftool is available (mandatory at startup) and extracts
// the embedded JPEG preview from a RAW file so the vision model can "see" it.
// Previews are captured in memory (exiftool's stdout) and never written to disk
// (feature 003, FR-005). It depends on no transport or storage package.
package rawpreview

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// ErrNoPreview means the RAW file exposes no embedded image under any supported
// tag. Callers treat it as "skip this photo as model input" (non-fatal), not as
// an operational failure.
var ErrNoPreview = errors.New("no embedded preview")

// previewTags are tried largest-first: a full-size embedded JPEG before the
// smaller preview, before the thumbnail of last resort (FR-004).
var previewTags = []string{"JpgFromRaw", "PreviewImage", "ThumbnailImage"}

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

// Extract returns the largest available embedded JPEG preview of rawPath, in
// memory. It tries previewTags in order and returns the first non-empty result.
// It returns ErrNoPreview when every tag is empty, or a wrapped error when
// exiftool cannot run or times out. No temporary file is written.
func (e *Extractor) Extract(ctx context.Context, rawPath string) ([]byte, error) {
	for _, tag := range previewTags {
		data, err := e.run(ctx, "-b", "-"+tag, rawPath)
		if err != nil {
			return nil, fmt.Errorf("extracting preview from %q: %w", rawPath, err)
		}
		if len(data) > 0 {
			e.log().Debug("extracted RAW preview", "file", rawPath, "tag", tag, "bytes", len(data))
			return data, nil
		}
	}
	return nil, ErrNoPreview
}

func (e *Extractor) log() *slog.Logger {
	if e.Logger != nil {
		return e.Logger
	}
	return slog.Default()
}

// run executes exiftool with the given args (no shell), bounded by e.Timeout,
// and returns its stdout. A missing tag yields empty stdout (and possibly a
// non-zero exit), which is reported as empty bytes — not an error; only a
// failure to start the process or a timeout is a hard error.
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
