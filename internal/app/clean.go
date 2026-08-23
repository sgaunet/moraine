package app

import (
	"context"
	"log/slog"

	"github.com/sgaunet/moraine/internal/clean"
	"github.com/sgaunet/moraine/internal/config"
)

// Clean runs the clean subcommand: it removes (or, in dry-run, reports) source
// originals already safely copied under the destination, logging each per-file
// decision and a final summary. It mirrors Organize as the transport-decoupled
// orchestration seam (Constitution Principle III): main.go only parses config and
// calls Clean. Per-file failures are non-fatal; a context cancellation is returned.
//
// onResult, when non-nil, receives every per-file Result, which is how the transport
// renders the machine-readable plan on stdout.
func Clean(
	ctx context.Context, cfg config.CleanConfig, logger *slog.Logger, onResult func(clean.Result),
) (clean.Summary, error) {
	if onResult == nil {
		onResult = func(clean.Result) {}
	}
	c := &clean.Cleaner{Source: cfg.Source, DestRoot: cfg.DestRoot, Delete: cfg.Delete}

	mode := "dry-run"
	if cfg.Delete {
		mode = "delete"
	}
	logger.Info("clean", "mode", mode, "source", cfg.Source, "dest", cfg.DestRoot)

	sum, err := c.Run(ctx, func(r clean.Result) {
		logClean(logger, r)
		onResult(r)
	})

	// Debug, not info: the summary is the run's stdout data now (Principle V).
	logger.Debug("summary",
		"deleted", sum.Deleted, "would_delete", sum.WouldDelete,
		"kept", sum.Kept, "errors", sum.Errors,
		"source_hashed", sum.SourceFilesHashed, "dest_hashed", sum.DestFilesHashed)
	return sum, err
}

// logClean writes one structured line per evaluated source file (FR-010): errors at
// error level, every other decision at info level, always with the reason and path
// so a user can reconstruct exactly what clean did (SC-004).
//
// Unlike sort's per-photo lines, these stay at info deliberately: a clean dry run's
// plan is the whole point of running it, not incidental progress noise.
func logClean(logger *slog.Logger, r clean.Result) {
	if r.Decision == clean.DecisionError {
		logger.Error("clean", "decision", string(r.Decision), "reason", r.Reason, "path", r.Path, "err", r.Err)
		return
	}
	logger.Info("clean", "decision", string(r.Decision), "reason", r.Reason, "path", r.Path)
}
