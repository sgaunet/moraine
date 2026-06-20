// Command moraine organizes photos automatically: it scans a source directory
// (or a single file), groups photos into events by capture time, classifies each
// group into a theme, and copies the photos into
// destination/<theme>/<year>/<year-month-day>/. Originals are preserved. Single
// static binary, pure Go, no CGo.
package main

import (
	"context"
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

func main() {
	cfg, err := config.Parse(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "erreur d'arguments :", err)
		fmt.Fprintln(os.Stderr, "usage : moraine [flags] <dossier-ou-fichier>")
		os.Exit(exitUsage)
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "erreur :", err)
		os.Exit(exitRuntime)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if _, err := app.Organize(ctx, cfg, logger); err != nil {
		fmt.Fprintln(os.Stderr, "erreur :", err)
		os.Exit(exitRuntime)
	}
	os.Exit(exitOK)
}
