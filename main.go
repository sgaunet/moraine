// Command moraine organizes photos automatically: it scans a source directory
// (or a single file), groups photos into events by capture time, classifies each
// group into a theme, and copies the photos into
// destination/<theme>/<year>/<year-month-day>/. Originals are preserved. Single
// static binary, pure Go, no CGo.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/sgaunet/moraine/internal/app"
	"github.com/sgaunet/moraine/internal/config"
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
	cfg, err := config.Parse(os.Args[1:])
	if err != nil {
		if errors.Is(err, config.ErrHelp) {
			config.WriteUsage(os.Stdout)
			os.Exit(exitOK)
		}
		fmt.Fprintln(os.Stderr, "argument error:", err)
		fmt.Fprintln(os.Stderr, "usage: moraine [options] <directory-or-file>")
		fmt.Fprintln(os.Stderr, "run `moraine -help` for detailed help")
		os.Exit(exitUsage)
	}
	if cfg.ShowVersion {
		fmt.Println("moraine", version)
		os.Exit(exitOK)
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(exitRuntime)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if _, err := app.Organize(ctx, cfg, logger); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(exitRuntime)
	}
	os.Exit(exitOK)
}
