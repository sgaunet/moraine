// Command moraine organizes photos automatically: it scans a source directory
// (or a single file), groups photos into events by capture time, classifies each
// group into a theme, and copies the photos into
// destination/<theme>/<year>/<year-month-day>/. Originals are preserved. Single
// static binary, pure Go, no CGo. exiftool is required to read RAW files.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/sgaunet/moraine/internal/app"
	"github.com/sgaunet/moraine/internal/config"
	"github.com/sgaunet/moraine/internal/rawpreview"
)

// Exit codes follow the CLI contract: 0 success, 1 runtime error, 2 usage error.
const (
	exitOK      = 0
	exitRuntime = 1
	exitUsage   = 2
)

// version is the build version, overridden with -ldflags "-X main.version=<v>".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable program body: it parses args, validates, ensures the
// mandatory exiftool dependency is present, then organizes. It returns the
// process exit code and writes user-facing output to stdout/stderr.
func run(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Parse(args)
	if err != nil {
		if errors.Is(err, config.ErrHelp) {
			config.WriteUsage(stdout)
			return exitOK
		}
		_, _ = fmt.Fprintln(stderr, "argument error:", err)
		_, _ = fmt.Fprintln(stderr, "usage: moraine [options] <directory-or-file>")
		_, _ = fmt.Fprintln(stderr, "run `moraine -help` for detailed help")
		return exitUsage
	}
	if cfg.ShowVersion {
		_, _ = fmt.Fprintln(stdout, "moraine", version)
		return exitOK
	}
	if err := cfg.Validate(); err != nil {
		_, _ = fmt.Fprintln(stderr, "error:", err)
		return exitRuntime
	}
	// exiftool is mandatory (RAW support): verify it before scanning, classifying,
	// or copying any file so a missing dependency fails fast (FR-003).
	if err := rawpreview.EnsureAvailable(cfg.ExifToolPath); err != nil {
		_, _ = fmt.Fprintln(stderr, "error:", err)
		return exitRuntime
	}

	logger := slog.New(slog.NewTextHandler(stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if _, err := app.Organize(ctx, cfg, logger); err != nil {
		_, _ = fmt.Fprintln(stderr, "error:", err)
		return exitRuntime
	}
	return exitOK
}
