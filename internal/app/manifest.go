package app

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/sgaunet/moraine/internal/classify"
	"github.com/sgaunet/moraine/internal/config"
	"github.com/sgaunet/moraine/internal/manifest"
	"github.com/sgaunet/moraine/internal/organize"
	"github.com/sgaunet/moraine/internal/photo"
)

// This file wires the run manifest into the organize pipeline: writing it (so a
// run can be undone and recognised later) and reading it back (so an incremental
// run can skip what it already placed). Both directions degrade to a warning: a
// missing or unreadable manifest costs bookkeeping, never a photo.

// recorder appends one manifest record per placement. It stops writing after the
// first failure — a manifest that cannot be written must not turn every remaining
// photo into an error — and a nil writer (a dry run) makes every method a no-op.
type recorder struct {
	writer *manifest.Writer
	logger *slog.Logger
	broken bool
}

// newRecorder returns the recorder for this run. A dry run writes nothing at all,
// not even a directory, so it gets a recorder with no writer.
func newRecorder(cfg config.Config, logger *slog.Logger) *recorder {
	if cfg.DryRun {
		return &recorder{logger: logger}
	}
	return &recorder{writer: manifest.New(cfg.DestRoot, cfg.Source, time.Now()), logger: logger}
}

// add records one placement. Photos the run never got to (a cancelled context)
// are not placements and are left out.
func (r *recorder) add(res organize.Result) {
	if r.writer == nil || r.broken || notAttempted(res.Err) {
		return
	}
	if err := r.writer.Add(record(res)); err != nil {
		r.broken = true
		r.logger.Warn("manifest unavailable: this run will not be undoable, "+
			"and --incremental will not see it", "err", err)
	}
}

// close flushes the manifest and reports where it landed, so a user who wants to
// undo the run knows what to look at.
func (r *recorder) close() {
	if r.writer == nil {
		return
	}
	if err := r.writer.Close(); err != nil {
		r.logger.Warn("manifest not fully written", "err", err)
		return
	}
	if path := r.writer.Path(); path != "" {
		r.logger.Info("manifest", "path", path)
	}
}

// record translates one placement into its manifest record. The size and
// modification time are read back from the file that is actually on disk: that
// pair is what `undo` checks before deleting anything and what an incremental run
// compares a source against.
func record(res organize.Result) manifest.Record {
	rec := manifest.Record{
		Source:    res.Source,
		Dest:      res.Dest,
		Theme:     res.Theme,
		Action:    string(res.Action),
		Companion: res.IsCompanion,
		Of:        res.Of,
	}
	if !res.Date.IsZero() {
		rec.Date = res.Date.Format(manifest.DateFormat)
	}
	if res.Err != nil {
		rec.Error = res.Err.Error()
		return rec
	}
	if info, err := os.Lstat(res.Dest); err == nil {
		rec.Size, rec.MTime = info.Size(), info.ModTime().UnixNano()
	}
	return rec
}

// placedIndex loads what previous runs recorded, for an incremental run only. An
// unreadable manifest tree degrades to a full run rather than failing it.
func placedIndex(cfg config.Config, logger *slog.Logger) *manifest.Index {
	if !cfg.Incremental {
		return nil
	}
	idx, err := manifest.Load(cfg.DestRoot)
	if err != nil {
		logger.Warn("manifests unreadable: running a full pass instead", "err", err)
		return nil
	}
	logger.Info("incremental", "known_sources", idx.Len(), "manifests_unreadable", idx.Skipped)
	return idx
}

// placedHook adapts the manifest index to the organizer's Placed seam, which
// speaks only of sizes and times so that organize needs no manifest dependency.
func placedHook(idx *manifest.Index) func(string) (organize.Placement, bool) {
	if idx == nil {
		return nil
	}
	return func(src string) (organize.Placement, bool) {
		rec, ok := idx.Lookup(src)
		if !ok || rec.Dest == "" {
			return organize.Placement{}, false
		}
		return organize.Placement{Dest: rec.Dest, Size: rec.Size, ModTime: time.Unix(0, rec.MTime)}, true
	}
}

// labelCluster assigns the cluster's theme. On an incremental run a cluster whose
// already-placed photos all agree on one (still configured) theme keeps it: that
// spares the model a round-trip and, more importantly, keeps a newly added photo
// filed with the event it belongs to instead of being classified on its own.
func labelCluster(
	ctx context.Context, c photo.Cluster, opts classify.Options, cfg config.Config, idx *manifest.Index,
) (string, classify.Method) {
	if theme := recordedTheme(idx, c); theme != "" && configuredTheme(theme, cfg) {
		return theme, classify.MethodManifest
	}
	return classify.Label(ctx, c, opts)
}

// recordedTheme returns the theme every already-placed photo of the cluster was
// filed under, or "" when none is recorded or they disagree.
func recordedTheme(idx *manifest.Index, c photo.Cluster) string {
	if idx == nil {
		return ""
	}
	theme := ""
	for _, p := range c.Photos {
		rec, ok := idx.Lookup(p.Path)
		if !ok || rec.Theme == "" {
			continue
		}
		if theme != "" && rec.Theme != theme {
			return ""
		}
		theme = rec.Theme
	}
	return theme
}

// configuredTheme reports whether a recorded theme is still part of this run's
// vocabulary. A user who changed --themes since the last run means the new list,
// so a theme that has disappeared from it is re-decided rather than reused.
func configuredTheme(theme string, cfg config.Config) bool {
	if theme == cfg.FallbackTheme {
		return true
	}
	for _, t := range cfg.Themes {
		if t == theme {
			return true
		}
	}
	return false
}
