// Package app wires moraine's organize pipeline (scan → EXIF → cluster →
// classify → copy) behind a single testable entrypoint, decoupled from the CLI
// transport (Constitution Principle III). main.go only parses config and calls
// Organize.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/sgaunet/moraine/internal/classify"
	"github.com/sgaunet/moraine/internal/cluster"
	"github.com/sgaunet/moraine/internal/config"
	"github.com/sgaunet/moraine/internal/exifmeta"
	"github.com/sgaunet/moraine/internal/organize"
	"github.com/sgaunet/moraine/internal/photo"
	"github.com/sgaunet/moraine/internal/rawpreview"
	"github.com/sgaunet/moraine/internal/scan"
)

// rawPreviewTimeout bounds each exiftool preview extraction.
const rawPreviewTimeout = 30 * time.Second

// Summary tallies what a run did, for the final log line and for tests.
type Summary struct {
	Groups  int
	Copied  int
	Skipped int
	Renamed int
	Errors  int
	// Companion (sidecar) outcomes, kept separate from photo outcomes (FR-010).
	CompanionsCopied  int
	CompanionsSkipped int
	CompanionsRenamed int
	CompanionsErrors  int
}

// Organize runs the full pipeline for cfg and returns a Summary. A directory
// source is organized in batch; a single file is organized on its own. Per-photo
// failures are logged and tallied but do not abort the run (FR-012).
func Organize(ctx context.Context, cfg config.Config, logger *slog.Logger) (Summary, error) {
	clusters, primaries, err := buildClusters(cfg, logger)
	if err != nil {
		return Summary{}, err
	}

	opts := classify.Options{Themes: cfg.Themes, Fallback: cfg.FallbackTheme}
	opts.Classifier = buildClassifier(ctx, cfg, logger)
	org := organize.New(cfg.DestRoot)
	org.Sidecars = cfg.Sidecars
	org.IsPrimary = func(p string) bool {
		_, ok := primaries[filepath.Clean(p)]
		return ok
	}

	var sum Summary
	for _, c := range clusters {
		if err := ctx.Err(); err != nil {
			return sum, err
		}
		theme, method := classify.Label(ctx, c, opts)
		logger.Info("group",
			"size", len(c.Photos), "method", string(method),
			"theme", theme, "date", c.Start.Format("2006-01-02"))
		sum.Groups++

		for _, r := range org.Place(ctx, c, theme) {
			tally(&sum, r, logger)
		}
	}

	logger.Info("summary",
		"groups", sum.Groups, "copied", sum.Copied, "skipped", sum.Skipped,
		"renamed", sum.Renamed, "errors", sum.Errors,
		"companions_copied", sum.CompanionsCopied, "companions_skipped", sum.CompanionsSkipped,
		"companions_renamed", sum.CompanionsRenamed, "companions_errors", sum.CompanionsErrors)
	return sum, nil
}

// buildClassifier constructs the Ollama classifier when the model stage is
// enabled, and runs a preflight so the logs always explain whether (and why)
// the LLM will or won't be used. On an unreachable endpoint or a missing model
// it logs an actionable message and returns nil — the run continues on the
// heuristic + fallback theme (a theme is always assigned, FR-005).
func buildClassifier(ctx context.Context, cfg config.Config, logger *slog.Logger) classify.Classifier {
	if cfg.Sample <= 0 {
		logger.Info("model stage disabled (-sample 0): heuristic + fallback only")
		return nil
	}
	oc := classify.NewOllama(cfg.OllamaURL, cfg.Model, cfg.Sample, cfg.Themes)
	oc.Logger = logger
	ex := rawpreview.NewExtractor(cfg.ExifToolPath, rawPreviewTimeout)
	ex.Logger = logger
	oc.Raw = ex // RAW photos are classified via their exiftool-extracted preview

	switch oc.Preflight(ctx) {
	case classify.StatusUnreachable:
		logger.Warn("Ollama unreachable: classifying via heuristic/fallback only; start it with `ollama serve`",
			"url", cfg.OllamaURL)
		return nil
	case classify.StatusModelMissing:
		logger.Warn("model missing from Ollama: pull it then re-run",
			"model", cfg.Model, "command", "ollama pull "+cfg.Model)
		return nil
	case classify.StatusReady:
		logger.Info("model ready", "url", cfg.OllamaURL, "model", cfg.Model)
		return oc
	}
	// Unreachable: Preflight returns only the three Status values handled above.
	return nil
}

// tally records one placement Result into the summary and logs it, routing
// companion (sidecar) outcomes into their own counters (FR-010).
func tally(sum *Summary, r organize.Result, logger *slog.Logger) {
	if r.IsCompanion {
		tallyCompanion(sum, r, logger)
		return
	}
	if r.Err != nil {
		sum.Errors++
		logger.Error("placement failed", "source", r.Source, "err", r.Err)
		return
	}
	switch r.Action {
	case organize.ActionCopied:
		sum.Copied++
	case organize.ActionSkippedIdentical:
		sum.Skipped++
	case organize.ActionRenamed:
		sum.Renamed++
	}
	logger.Info("photo", "action", string(r.Action), "source", r.Source, "dest", r.Dest)
}

// tallyCompanion records one companion Result and logs it. A per-companion
// failure is non-fatal (FR-008): counted and logged, the run continues.
func tallyCompanion(sum *Summary, r organize.Result, logger *slog.Logger) {
	if r.Err != nil {
		sum.CompanionsErrors++
		logger.Error("companion failed", "source", r.Source, "of", r.Of, "err", r.Err)
		return
	}
	switch r.Action {
	case organize.ActionCopied:
		sum.CompanionsCopied++
	case organize.ActionSkippedIdentical:
		sum.CompanionsSkipped++
	case organize.ActionRenamed:
		sum.CompanionsRenamed++
	}
	logger.Info("companion", "action", string(r.Action), "source", r.Source, "dest", r.Dest, "of", r.Of)
}

// buildClusters produces the clusters to organize plus the set of scanned primary
// photo paths (cleaned absolute), used to keep a photo from being copied as its
// own companion (FR-006). A directory source yields many clusters; a single file
// yields exactly one and a one-element primary set.
func buildClusters(cfg config.Config, logger *slog.Logger) ([]photo.Cluster, map[string]struct{}, error) {
	if !cfg.SourceIsDir {
		clusters, err := singleCluster(cfg, logger)
		if err != nil {
			return nil, nil, err
		}
		return clusters, map[string]struct{}{filepath.Clean(cfg.Source): {}}, nil
	}

	found, err := scan.Scan(cfg.Source, cfg.DestRoot)
	if err != nil {
		return nil, nil, err
	}
	logger.Info("scan", "images", len(found), "excluded_dest", cfg.DestRoot)

	primaries := make(map[string]struct{}, len(found))
	for _, f := range found {
		primaries[filepath.Clean(f.Path)] = struct{}{}
	}

	photos := readMeta(found, logger)
	logger.Info("exif", "read", len(photos), "of", len(found), "raw", countRAW(photos))

	clusters := cluster.Cluster(photos, cfg.Gap)
	logger.Info("cluster", "photos", len(photos), "groups", len(clusters), "gap", cfg.Gap.String())
	return clusters, primaries, nil
}

// countRAW reports how many photos are RAW, for the run logs (FR-010).
func countRAW(photos []photo.Photo) int {
	n := 0
	for _, p := range photos {
		if p.Format.IsRAW() {
			n++
		}
	}
	return n
}

// singleCluster reads one file and wraps it as a one-photo cluster (single-photo mode).
func singleCluster(cfg config.Config, logger *slog.Logger) ([]photo.Cluster, error) {
	format, ok := photo.FormatFromExt(cfg.Source)
	if !ok {
		return nil, fmt.Errorf("unsupported format for %q (expected JPEG/PNG/HEIC/RAW)", cfg.Source)
	}
	p, err := exifmeta.Read(cfg.Source, format)
	if err != nil {
		return nil, fmt.Errorf("reading %q: %w", cfg.Source, err)
	}
	logger.Info("single photo", "file", cfg.Source, "date", p.Taken.Format("2006-01-02"))
	return []photo.Cluster{{Photos: []photo.Photo{p}, Start: p.Taken, End: p.Taken}}, nil
}

// readMeta reads EXIF metadata for every file using a bounded worker pool.
// Files whose metadata cannot be read are skipped with a warning (FR-012).
func readMeta(found []scan.Found, logger *slog.Logger) []photo.Photo {
	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		workers = 1
	}
	sem := make(chan struct{}, workers)
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		photos = make([]photo.Photo, 0, len(found))
	)
	for _, f := range found {
		wg.Add(1)
		sem <- struct{}{}
		go func(f scan.Found) {
			defer wg.Done()
			defer func() { <-sem }()
			p, err := exifmeta.Read(f.Path, f.Format)
			if err != nil {
				logger.Warn("file skipped", "file", f.Path, "err", err)
				return
			}
			mu.Lock()
			photos = append(photos, p)
			mu.Unlock()
		}(f)
	}
	wg.Wait()
	return photos
}
