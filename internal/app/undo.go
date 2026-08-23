package app

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/sgaunet/moraine/internal/config"
	"github.com/sgaunet/moraine/internal/manifest"
	"github.com/sgaunet/moraine/internal/undo"
)

// Undo runs the undo subcommand: it resolves the most recent run manifest under
// the destination and removes (or, in dry-run, reports) the copies that run made.
// It mirrors Organize and Clean as a transport-decoupled orchestration seam
// (Constitution Principle III). Per-file failures are non-fatal; a context
// cancellation is returned with the partial summary.
//
// onResult, when non-nil, receives every per-file Result, which is how the
// transport renders the machine-readable plan on stdout.
func Undo(
	ctx context.Context, cfg config.UndoConfig, logger *slog.Logger, onResult func(undo.Result),
) (undo.Summary, error) {
	if onResult == nil {
		onResult = func(undo.Result) {}
	}

	path, err := manifest.Latest(cfg.DestRoot)
	if err != nil {
		return undo.Summary{}, err
	}
	if path == "" {
		return undo.Summary{}, fmt.Errorf(
			"no run to undo under %q: no manifest recorded there (a run made before manifests "+
				"existed, or an already undone one)", cfg.DestRoot)
	}
	run, err := manifest.ReadRun(path)
	if err != nil {
		return undo.Summary{}, err
	}

	mode := "dry-run"
	if cfg.Delete {
		mode = "delete"
	}
	logger.Info("undo", "mode", mode, "run", run.ID(), "records", len(run.Records),
		"dest", cfg.DestRoot, "sorted", run.Header.Source, "started", run.Header.Started)
	if run.Skipped > 0 {
		logger.Warn("manifest has unreadable lines: those placements cannot be undone",
			"run", run.ID(), "lines", run.Skipped)
	}
	if d := run.Header.Dest; d != "" && filepath.Clean(d) != filepath.Clean(cfg.DestRoot) {
		logger.Warn("manifest was written for another destination: nothing outside this one will be touched",
			"manifest_dest", d, "dest", cfg.DestRoot)
	}

	u := &undo.Undoer{DestRoot: cfg.DestRoot, Delete: cfg.Delete}
	sum, err := u.Run(ctx, run, func(r undo.Result) {
		logUndo(logger, r)
		onResult(r)
	})

	if err == nil && sum.Removed+sum.WouldRemove == 0 {
		// A re-run that only recognised existing copies has nothing to give back.
		// Saying so beats leaving the user to wonder why the tally is all zeros.
		logger.Info("this run created no files of its own; run undo again to step back to the run before it",
			"run", run.ID())
	}

	// Debug, not info: the summary is the run's stdout data (Principle V).
	logger.Debug("summary",
		"removed", sum.Removed, "would_remove", sum.WouldRemove,
		"kept", sum.Kept, "errors", sum.Errors, "dirs_pruned", sum.DirsPruned)
	return sum, err
}

// logUndo writes one structured line per evaluated file. Like clean's, these stay
// at info: the plan of what a dry run would remove is the point of running it.
func logUndo(logger *slog.Logger, r undo.Result) {
	if r.Decision == undo.DecisionError {
		logger.Error("undo", "decision", string(r.Decision), "reason", r.Reason, "path", r.Path, "err", r.Err)
		return
	}
	logger.Info("undo", "decision", string(r.Decision), "reason", r.Reason, "path", r.Path)
}
