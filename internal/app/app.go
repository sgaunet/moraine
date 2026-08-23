// Package app wires moraine's organize pipeline (scan → EXIF → cluster →
// classify → copy) behind a single testable entrypoint, decoupled from the CLI
// transport (Constitution Principle III). main.go only parses config and calls
// Organize.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sgaunet/moraine/internal/classify"
	"github.com/sgaunet/moraine/internal/cluster"
	"github.com/sgaunet/moraine/internal/config"
	"github.com/sgaunet/moraine/internal/exifmeta"
	"github.com/sgaunet/moraine/internal/heicpreview"
	"github.com/sgaunet/moraine/internal/organize"
	"github.com/sgaunet/moraine/internal/photo"
	"github.com/sgaunet/moraine/internal/rawpreview"
	"github.com/sgaunet/moraine/internal/scan"
)

// rawPreviewTimeout bounds each exiftool preview extraction.
const rawPreviewTimeout = 30 * time.Second

// heicConvertTimeout bounds each HEIC conversion. It is more generous than the
// exiftool bound because a converter decodes the whole image rather than copying
// bytes out of it.
const heicConvertTimeout = 60 * time.Second

// Summary tallies what a run did, for the final log line and for tests.
type Summary struct {
	// Input outcomes: how many images the scan found, and how many of those the
	// run could not read metadata from. A file counted as unreadable was never
	// placed, so it appears in no other counter — without these two, an input the
	// run silently dropped left no trace in the result at all.
	Scanned    int
	Unreadable int
	Groups     int
	Copied     int
	Skipped    int
	Renamed    int
	Errors     int
	// Companion (sidecar) outcomes, kept separate from photo outcomes (FR-010).
	CompanionsCopied  int
	CompanionsSkipped int
	CompanionsRenamed int
	CompanionsErrors  int
	// Volume moved and volume spared. BytesCopied is what was actually written
	// (copies and renames alike; on a dry run, what would have been written);
	// BytesSkipped is what an already-identical destination saved re-writing. Counts
	// alone cannot answer "was this re-run worth anything" — a run that skips 12
	// files says nothing about whether that was 12 KB or 12 GB.
	BytesCopied            int64
	BytesSkipped           int64
	CompanionsBytesCopied  int64
	CompanionsBytesSkipped int64
	// Events describes each event the run placed, in the order it placed them. It is
	// bounded by the number of events, not by the number of photos, which is why the
	// run can afford to keep it while it deliberately does not keep per-file records.
	Events []Event
}

// Event is one placed event: how its theme was decided, how big it was, and what
// placing it cost. Until now this was logged once and thrown away, so a run could
// report totals but never which event produced them.
//
// The outcome counters cover every file placed for the event — photos and their
// companions alike — so Copied can exceed Photos. Bytes follow Summary's meaning.
type Event struct {
	Theme        string
	Method       string // how the theme was decided (classify.Method)
	Photos       int    // photos in the cluster
	Start        time.Time
	End          time.Time
	Copied       int
	Skipped      int
	Renamed      int
	Errors       int
	BytesCopied  int64
	BytesSkipped int64
}

// Organize runs the full pipeline for cfg and returns a Summary. A directory
// source is organized in batch; a single file is organized on its own. Per-photo
// failures are logged and tallied but do not abort the run (FR-012).
//
// onResult, when non-nil, receives every placement Result as it happens — the seam
// the transport uses to render the run's machine-readable stdout, mirroring
// clean.Cleaner.Run. A cancelled context stops the run and is returned alongside
// the partial Summary, so the caller can report what was done before the interrupt.
func Organize(
	ctx context.Context, cfg config.Config, logger *slog.Logger, onResult func(organize.Result),
) (Summary, error) {
	if onResult == nil {
		onResult = func(organize.Result) {}
	}

	in, err := buildClusters(cfg, logger)
	if err != nil {
		return Summary{}, err
	}

	opts := classify.Options{
		Themes:            cfg.Themes,
		Fallback:          cfg.FallbackTheme,
		MountainAltitudeM: cfg.MountainAltitude,
		MinConfidence:     cfg.MinConfidence,
	}
	opts.Classifier = buildClassifier(ctx, cfg, logger)
	org := organize.New(cfg.DestRoot)
	org.Template = cfg.PathTemplate
	org.Sidecars = cfg.Sidecars
	org.DryRun = cfg.DryRun
	placed := placedIndex(cfg, logger)
	org.Placed = placedHook(placed)
	org.IsPrimary = func(p string) bool {
		_, ok := in.primaries[filepath.Clean(p)]
		return ok
	}

	// The manifest is the record of what this run copied: `undo` reads it to remove
	// exactly those files, and a later --incremental run to recognise them.
	rec := newRecorder(cfg, logger)
	defer rec.close()

	// Set before the loop: an interrupted run must still report what it was given.
	sum := Summary{Scanned: in.scanned, Unreadable: in.unreadable}
	for _, c := range in.clusters {
		if err := ctx.Err(); err != nil {
			return sum, err
		}
		theme, method := labelCluster(ctx, c, opts, cfg, placed)
		logger.Info("group",
			"size", len(c.Photos), "method", string(method),
			"theme", theme, "date", c.Start.Format("2006-01-02"))
		sum.Groups++

		ev := Event{
			Theme: theme, Method: string(method), Photos: len(c.Photos),
			Start: c.Start, End: c.End,
		}
		for _, r := range org.Place(ctx, c, theme) {
			tally(&sum, r, logger)
			tallyEvent(&ev, r)
			rec.add(r)
			onResult(r)
		}
		sum.Events = append(sum.Events, ev)
	}

	// Debug, not info: the summary is the run's stdout data now (Principle V), so
	// logging it again would show an interactive user the same numbers twice.
	logger.Debug("summary",
		"scanned", sum.Scanned, "unreadable", sum.Unreadable,
		"groups", sum.Groups, "copied", sum.Copied, "skipped", sum.Skipped,
		"renamed", sum.Renamed, "errors", sum.Errors,
		"companions_copied", sum.CompanionsCopied, "companions_skipped", sum.CompanionsSkipped,
		"companions_renamed", sum.CompanionsRenamed, "companions_errors", sum.CompanionsErrors,
		"bytes_copied", sum.BytesCopied, "bytes_skipped", sum.BytesSkipped)

	// Report the cancellation even when it arrived inside the last (or only) cluster.
	// The loop above only checks between clusters, so a single-event import — one
	// cluster holding every photo — would otherwise finish "successfully" having
	// placed a handful of photos and abandoned the rest.
	return sum, ctx.Err()
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
	oc.Vote = cfg.Vote
	ex := rawpreview.NewExtractor(cfg.ExifToolPath, rawPreviewTimeout)
	ex.Logger = logger
	oc.RawPreview = ex // a RAW is classified via its embedded JPEG preview
	attachHEICConverter(oc, logger)

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

// attachHEICConverter gives the classifier a way to see HEIC photos, when the
// system has one. Unlike RAW, a HEIC embeds no JPEG for exiftool to copy out —
// its derived images are HEVC — so decoding it needs a real converter. None is
// required: without one, HEIC photos are still scanned, dated and copied, and only
// their classification falls back, so this is a log line rather than an error.
func attachHEICConverter(oc *classify.OllamaClassifier, logger *slog.Logger) {
	conv := heicpreview.Detect(heicConvertTimeout)
	if conv == nil {
		logger.Info("no HEIC converter found: HEIC groups fall back to the heuristic or fallback theme",
			"install_one_of", strings.Join(heicpreview.Installable(), ", "))
		return
	}
	conv.Logger = logger
	// Assigned only when non-nil: a nil *Converter in the interface would read as
	// "configured" everywhere downstream.
	oc.HEICPreview = conv
	logger.Info("HEIC converter", "tool", conv.Name())
}

// tally records one placement Result into the summary and logs it, routing
// companion (sidecar) outcomes into their own counters (FR-010). Per-file lines are
// logged at debug: a real library produces thousands of them, and the run's result
// is carried by the Summary on stdout, so the default output stays readable.
func tally(sum *Summary, r organize.Result, logger *slog.Logger) {
	if r.IsCompanion {
		tallyCompanion(sum, r, logger)
		return
	}
	if r.Err != nil {
		if notAttempted(r.Err) {
			logger.Debug("photo not attempted", "source", r.Source, "err", r.Err)
			return
		}
		sum.Errors++
		logger.Error("placement failed", "source", r.Source, "err", r.Err)
		return
	}
	switch r.Action {
	case organize.ActionCopied:
		sum.Copied++
		sum.BytesCopied += r.Size
	case organize.ActionSkippedIdentical:
		sum.Skipped++
		sum.BytesSkipped += r.Size
	case organize.ActionRenamed:
		sum.Renamed++
		sum.BytesCopied += r.Size
	}
	logger.Debug("photo", "action", string(r.Action), "source", r.Source, "dest", r.Dest, "bytes", r.Size)
}

// tallyCompanion records one companion Result and logs it. A per-companion
// failure is non-fatal (FR-008): counted and logged, the run continues.
func tallyCompanion(sum *Summary, r organize.Result, logger *slog.Logger) {
	if r.Err != nil {
		if notAttempted(r.Err) {
			logger.Debug("companion not attempted", "source", r.Source, "of", r.Of, "err", r.Err)
			return
		}
		sum.CompanionsErrors++
		logger.Error("companion failed", "source", r.Source, "of", r.Of, "err", r.Err)
		return
	}
	switch r.Action {
	case organize.ActionCopied:
		sum.CompanionsCopied++
		sum.CompanionsBytesCopied += r.Size
	case organize.ActionSkippedIdentical:
		sum.CompanionsSkipped++
		sum.CompanionsBytesSkipped += r.Size
	case organize.ActionRenamed:
		sum.CompanionsRenamed++
		sum.CompanionsBytesCopied += r.Size
	}
	logger.Debug("companion", "action", string(r.Action), "source", r.Source, "dest", r.Dest,
		"of", r.Of, "bytes", r.Size)
}

// tallyEvent records one Result against the event it belongs to. Photos and
// companions share these counters: what a reader wants per event is what the event
// cost, not a second photo/companion split of the totals above. Cancelled work is
// left out for the same reason tally leaves it out — nothing was attempted.
func tallyEvent(ev *Event, r organize.Result) {
	if r.Err != nil {
		if !notAttempted(r.Err) {
			ev.Errors++
		}
		return
	}
	switch r.Action {
	case organize.ActionCopied:
		ev.Copied++
		ev.BytesCopied += r.Size
	case organize.ActionSkippedIdentical:
		ev.Skipped++
		ev.BytesSkipped += r.Size
	case organize.ActionRenamed:
		ev.Renamed++
		ev.BytesCopied += r.Size
	}
}

// notAttempted reports whether a Result failed because the run was cancelled rather
// than because the file could not be placed. organize.Place records the context
// error against every photo it did not get to, and counting those as failures would
// make an interrupt look like a library full of broken photos.
func notAttempted(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// input is what the scan and EXIF stages produce for the rest of the pipeline: the
// clusters to organize, the set of scanned primary photo paths (cleaned absolute)
// that keeps a photo from being copied as its own companion (FR-006), and the two
// input tallies the run Summary reports.
type input struct {
	clusters   []photo.Cluster
	primaries  map[string]struct{}
	scanned    int // images the scan discovered
	unreadable int // discovered images whose metadata could not be read
}

// buildClusters runs the scan and EXIF stages. A directory source yields many
// clusters; a single file yields exactly one and a one-element primary set.
func buildClusters(cfg config.Config, logger *slog.Logger) (input, error) {
	if !cfg.SourceIsDir {
		clusters, err := singleCluster(cfg, logger)
		if err != nil {
			// A single-photo run has nowhere to record a per-file failure, so an
			// unreadable file is fatal here rather than tallied.
			return input{}, err
		}
		return input{
			clusters:  clusters,
			primaries: map[string]struct{}{filepath.Clean(cfg.Source): {}},
			scanned:   1,
		}, nil
	}

	found, err := scan.Scan(cfg.Source, cfg.DestRoot, logger)
	if err != nil {
		return input{}, err
	}
	logger.Info("scan", "images", len(found), "excluded_dest", cfg.DestRoot)

	primaries := make(map[string]struct{}, len(found))
	for _, f := range found {
		primaries[filepath.Clean(f.Path)] = struct{}{}
	}

	photos := readMeta(found, cfg.Jobs, logger)
	logger.Info("exif", "read", len(photos), "of", len(found), "raw", countRAW(photos))

	clusters := cluster.Cluster(photos, cfg.Gap)
	logger.Info("cluster", "photos", len(photos), "groups", len(clusters), "gap", cfg.Gap.String())
	// readMeta drops exactly the files it could not read, so the shortfall is the
	// unreadable count — no second tally to keep in step with it.
	return input{
		clusters:   clusters,
		primaries:  primaries,
		scanned:    len(found),
		unreadable: len(found) - len(photos),
	}, nil
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

// readMeta reads EXIF metadata for every file using a bounded worker pool of jobs
// workers, or one per GOMAXPROCS when jobs is 0. Turning it down throttles a network
// drive; turning it up can pay off on fast local storage. Files whose metadata
// cannot be read are skipped with a warning (FR-012).
func readMeta(found []scan.Found, jobs int, logger *slog.Logger) []photo.Photo {
	workers := jobs
	if workers < 1 {
		workers = runtime.GOMAXPROCS(0)
	}
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
	// The pool finishes in whatever order the filesystem answers, so restore the
	// lexical order scan.Scan produced. cluster.Cluster orders by path within one
	// capture time anyway; keeping the pipeline's own data in a fixed order is what
	// makes the logs and the per-file stdout records reproducible too.
	slices.SortFunc(photos, func(a, b photo.Photo) int { return strings.Compare(a.Path, b.Path) })
	return photos
}
