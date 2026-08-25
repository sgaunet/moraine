// Package heicpreview turns a HEIC/HEIF file into JPEG bytes the vision model can
// read, by shelling out to whichever image converter the system already has.
//
// It exists because HEIC cannot be handled the way RAW is. A camera RAW carries a
// full JPEG preview that exiftool can copy straight out; a HEIC written by an
// iPhone carries only HEVC-coded derived images, so `exiftool -b -PreviewImage`
// and its siblings return nothing at all. Decoding HEVC in pure Go is not on the
// table (Constitution: no CGo), which leaves an external converter as the only way
// to show the model what a HEIC actually depicts.
//
// The converter is entirely optional, unlike exiftool: when none is installed,
// HEIC photos are still scanned, dated, organised and copied — they simply reach
// no model, exactly as before this package existed. That is why Detect reports
// "none installed" rather than returning an error.
//
// Output is captured from stdout where the tool supports it and via a temporary
// file where it does not; the temporary file is removed before Extract returns.
// Full-resolution JPEG is requested in every case: the caller owns the downscale,
// so the size sent to the model is decided in one place rather than four.
package heicpreview

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// tool is one supported converter: how to invoke it, and how to get its output
// back. dst is the path the tool must write to, and is empty for stdout tools.
type tool struct {
	name   string
	stdout bool
	args   func(src, dst string) []string
}

// tools are probed in this order, fastest and most-likely-present first. sips is
// part of macOS, so a Mac needs nothing installed; the rest cover Linux, where
// libheif's heif-convert, ffmpeg and ImageMagick are the usual ways to read HEIC.
var tools = []tool{
	{name: "sips", args: func(src, dst string) []string {
		return []string{"-s", "format", "jpeg", src, "--out", dst}
	}},
	{name: "heif-convert", args: func(src, dst string) []string {
		return []string{"-q", "90", src, dst}
	}},
	{name: "ffmpeg", stdout: true, args: func(src, _ string) []string {
		return []string{"-loglevel", "error", "-i", src, "-f", "mjpeg", "-"}
	}},
	{name: "magick", stdout: true, args: func(src, _ string) []string {
		return []string{src, "jpeg:-"}
	}},
}

// Installable lists the converters Detect looks for, for an actionable log line
// when none of them is present.
func Installable() []string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.name)
	}
	return names
}

// Converter decodes HEIC files with one external program.
type Converter struct {
	tool    tool
	path    string        // resolved executable
	Timeout time.Duration // per-invocation bound; <= 0 means no extra bound beyond ctx
	Logger  *slog.Logger
}

// Detect returns a Converter for the first supported program found on PATH, and
// reports whether one was found at all.
//
// The second result is the point: a *Converter assigned into an interface — which
// is exactly what the one production caller does with classify.PreviewExtractor —
// yields a non-nil interface holding a nil pointer, and reads as "configured"
// everywhere downstream. Answering with a bool puts that check in the signature
// instead of in a doc comment nobody is obliged to read.
func Detect(timeout time.Duration) (*Converter, bool) {
	for _, t := range tools {
		path, err := exec.LookPath(t.name)
		if err != nil {
			continue
		}
		return &Converter{tool: t, path: path, Timeout: timeout, Logger: slog.Default()}, true
	}
	return nil, false
}

// Name reports which converter was found, for the run logs.
func (c *Converter) Name() string {
	if c == nil {
		return ""
	}
	return c.tool.name
}

// Extract returns the JPEG rendering of the HEIC file at path, at its full
// resolution. It returns a wrapped error when the converter cannot run, times
// out, or produces nothing usable; the caller treats that as "skip this photo as
// model input", never as a reason to fail the run.
func (c *Converter) Extract(ctx context.Context, path string) ([]byte, error) {
	if c == nil {
		return nil, errors.New("no HEIC converter available")
	}
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}

	data, err := c.convert(ctx, path)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("converting %q with %s: no image produced", path, c.tool.name)
	}
	c.log().Debug("converted HEIC", "file", path, "tool", c.tool.name, "bytes", len(data))
	return data, nil
}

// convert runs the tool and returns its JPEG output, from stdout or from the
// temporary file the tool was told to write.
func (c *Converter) convert(ctx context.Context, src string) ([]byte, error) {
	dst := ""
	if !c.tool.stdout {
		f, err := os.CreateTemp("", "moraine-heic-*.jpg")
		if err != nil {
			return nil, fmt.Errorf("creating a scratch file for %q: %w", src, err)
		}
		dst = f.Name()
		_ = f.Close()
		defer func() { _ = os.Remove(dst) }()
	}

	// Normalised here rather than in each tool's args func, so a converter added
	// later inherits the guard. dst is always an os.CreateTemp path and needs none.
	cmd := exec.CommandContext(ctx, c.path, c.tool.args(operand(src), dst)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("%s timed out converting %q: %w", c.tool.name, src, ctx.Err())
	}
	if err != nil {
		return nil, fmt.Errorf("running %s on %q: %w (stderr: %s)",
			c.tool.name, src, err, strings.TrimSpace(stderr.String()))
	}
	if c.tool.stdout {
		return stdout.Bytes(), nil
	}

	data, err := os.ReadFile(dst)
	if err != nil {
		return nil, fmt.Errorf("reading what %s wrote for %q: %w", c.tool.name, filepath.Base(src), err)
	}
	return data, nil
}

// operand returns src in the one form no option parser can mistake for a flag. An
// explicit "./" is the only neutraliser all four converters accept: heif-convert
// honours a "--" terminator, sips answers `unknown function "--"`, and ffmpeg reads
// the "-i" after it as an output name. filepath.Join cannot be used — it cleans the
// "./" straight back off.
//
// Today every path reaching here is absolute, because config.Config.Validate runs
// filepath.Abs on the source root. This does not depend on that: the guard belongs
// next to the exec call, not several packages away.
func operand(src string) string {
	if strings.HasPrefix(src, "-") {
		return "." + string(filepath.Separator) + src
	}
	return src
}

func (c *Converter) log() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}
