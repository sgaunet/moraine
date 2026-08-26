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
	"github.com/sgaunet/moraine/internal/diskspace"
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
	// Moved counts the source files --move removed after verifying their copies. It
	// is a subset of Copied+Renamed, never of Skipped: a skip verifies nothing this
	// run, so it never removes an original.
	Moved int
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
//
// prog, when non-nil, receives the run's progress so the transport can draw it. It
// is reported from several goroutines and is the one seam here that must be safe for
// concurrent use; see Progress.
func Organize(
	ctx context.Context, cfg config.Config, logger *slog.Logger, onResult func(organize.Result),
	prog Progress,
) (Summary, error) {
	if onResult == nil {
		onResult = func(organize.Result) {}
	}
	if prog == nil {
		prog = noProgress{}
	}

	in, err := buildClusters(ctx, cfg, logger, prog)
	if err != nil {
		// An interrupted scan or EXIF stage still reports what it managed to take in.
		// On any other error `in` is zero, so this is the Summary{} it always was.
		return Summary{Scanned: in.scanned, Unreadable: in.unreadable}, err
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
	org.Move = cfg.Move
	org.DryRun = cfg.DryRun
	org.Jobs = cfg.Jobs
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

	// Classification is the run's one network round-trip and placement is its one
	// bulk-I/O stage, so overlapping them is free wall-clock. The loop still consumes
	// events strictly in order, which is what keeps Groups, Events and the onResult
	// stream — the stdout contract — identical to a serial run. The only visible
	// difference is in the debug log, where the model lines for the next event now
	// interleave with the copy lines for this one.
	labels, stopAhead := labelAhead(ctx, in.clusters, opts, cfg, placed, prog)
	defer stopAhead()

	// Opened after the classifier so the two phases are reported in pipeline order,
	// and once for the whole run rather than per event: the total is known here (the
	// clusters are built), and a count that restarted at every event would say
	// nothing about how much of the library is left.
	copies := prog.Begin(PhaseCopy, countPhotos(in.clusters))
	defer copies.Close()
	org.OnUnitDone = copies.Inc

	// Set before the loop: an interrupted run must still report what it was given.
	sum := Summary{Scanned: in.scanned, Unreadable: in.unreadable}
	for l := range labels {
		if err := ctx.Err(); err != nil {
			return sum, err
		}
		logger.Info("group",
			"size", len(l.cluster.Photos), "method", string(l.method),
			"theme", l.theme, "date", l.cluster.Start.Format("2006-01-02"))
		sum.Groups++

		ev := Event{
			Theme: l.theme, Method: string(l.method), Photos: len(l.cluster.Photos),
			Start: l.cluster.Start, End: l.cluster.End,
		}
		for _, r := range org.Place(ctx, l.cluster, l.theme) {
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
		"bytes_copied", sum.BytesCopied, "bytes_skipped", sum.BytesSkipped,
		"moved", sum.Moved)

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
	conv, ok := heicpreview.Detect(heicConvertTimeout)
	if !ok {
		logger.Info("no HEIC converter found: HEIC groups fall back to the heuristic or fallback theme",
			"install_one_of", strings.Join(heicpreview.Installable(), ", "))
		return
	}
	conv.Logger = logger
	// Assigned only when one was found: a nil *Converter in the interface would
	// read as "configured" everywhere downstream.
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
	if r.Moved {
		sum.Moved++
	}
	logger.Debug("photo", "action", string(r.Action), "source", r.Source, "dest", r.Dest,
		"bytes", r.Size, "moved", r.Moved)
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
	if r.Moved {
		sum.Moved++
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
//
// Both stages honour ctx. Together they are frequently the longest phase of a large
// run, and until they did, a Ctrl-C aimed at the wrong folder had no effect until the
// whole library had been walked and read. A cancelled run returns the context error
// with the counts it had reached, so the caller can still report what it took in.
func buildClusters(
	ctx context.Context, cfg config.Config, logger *slog.Logger, prog Progress,
) (input, error) {
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

	found, err := scan.Scan(ctx, cfg.Source, cfg.DestRoot, logger)
	if err != nil {
		return input{}, err
	}
	logger.Info("scan", "images", len(found), "excluded_dest", cfg.DestRoot)

	primaries := make(map[string]struct{}, len(found))
	var needed int64
	for _, f := range found {
		primaries[filepath.Clean(f.Path)] = struct{}{}
		needed += f.Size
	}
	checkFreeSpace(cfg.DestRoot, needed, logger)

	photos, unreadable := readMeta(ctx, found, cfg.Jobs, logger, prog)
	logger.Info("exif", "read", len(photos), "of", len(found), "raw", countRAW(photos))

	// readMeta reports its own failures rather than leaving them to be derived from
	// the shortfall: once it can stop early, len(found)-len(photos) also counts every
	// file the cancellation never reached, which would report an interrupted run as a
	// library full of unreadable photos — the mistake notAttempted exists to avoid.
	in := input{primaries: primaries, scanned: len(found), unreadable: unreadable}
	if err := ctx.Err(); err != nil {
		// Returned here rather than left to the label loop: buildClassifier's Ollama
		// preflight sits between the two, and a Ctrl-C should not be answered with a
		// warning that Ollama is unreachable on the way out.
		return in, err
	}

	in.clusters = cluster.Cluster(photos, cfg.Gap)
	logger.Info("cluster", "photos", len(photos), "groups", len(in.clusters), "gap", cfg.Gap.String())
	return in, nil
}

// checkFreeSpace compares what the scan found against what the destination filesystem
// has free, and warns once when it does not fit. Without it a full disk announces
// itself as one "placement failed" per photo, with nothing in the run saying that the
// disk — and not the photos — was the problem.
//
// It never aborts, and that is a decision rather than an omission: the estimate is an
// upper bound in two directions. It cannot see companion files, which organize only
// discovers per source directory, and it counts every photo even though a re-run skips
// the ones already placed byte-identically. On a nearly-full destination holding an
// already-complete archive, a size-based abort would refuse a run that writes nothing
// at all. Report the numbers and let the run decide for itself, photo by photo.
func checkFreeSpace(destRoot string, needed int64, logger *slog.Logger) {
	avail, err := diskspace.Available(destRoot)
	if err != nil {
		// Debug, not warn: an advisory the platform cannot provide is not a problem
		// with the run, and a warning on every run of a build without statfs would be
		// noise. Logged rather than swallowed all the same.
		logger.Debug("free space unknown", "dest", destRoot, "err", err)
		return
	}
	logger.Debug("space", "needed_bytes", needed, "available_bytes", avail, "dest", destRoot)
	if needed > 0 && uint64(needed) > avail {
		logger.Warn("destination may be too small: the run will copy what fits",
			"needed_bytes", needed, "available_bytes", avail, "dest", destRoot)
	}
}

// countPhotos totals the photos the clusters hold, which is what the copy phase
// will place. Companions are deliberately not counted: they are discovered per
// source directory during placement, so their number is not knowable here, and each
// one rides along with the photo it belongs to.
func countPhotos(clusters []photo.Cluster) int {
	n := 0
	for _, c := range clusters {
		n += len(c.Photos)
	}
	return n
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
		// A crash in the EXIF parser still leaves a usable photo, dated by the file
		// name or its mtime, and a single-photo run has exactly one file to organize:
		// dropping it here would turn a third-party parser bug into "moraine did
		// nothing". Say what happened and carry on with what the fallback tiers gave.
		if !errors.Is(err, exifmeta.ErrEXIFPanic) {
			return nil, fmt.Errorf("reading %q: %w", cfg.Source, err)
		}
		logger.Warn("exif parser crashed on this file: dating it by name or mtime instead",
			"file", cfg.Source, "err", err)
	}
	logger.Info("single photo", "file", cfg.Source, "date", p.Taken.Format("2006-01-02"))
	return []photo.Cluster{{Photos: []photo.Photo{p}, Start: p.Taken, End: p.Taken}}, nil
}

// readMeta reads EXIF metadata for every file using a bounded worker pool of jobs
// workers, or one per GOMAXPROCS when jobs is 0. Turning it down throttles a network
// drive; turning it up can pay off on fast local storage. Files whose metadata
// cannot be read are skipped with a warning (FR-012) and counted in the second
// return value.
//
// Cancelling ctx stops the pool from taking on new files; the workers already running
// finish theirs, which is bounded by a single EXIF read each.
func readMeta(
	ctx context.Context, found []scan.Found, jobs int, logger *slog.Logger, prog Progress,
) ([]photo.Photo, int) {
	workers := jobs
	if workers < 1 {
		workers = runtime.GOMAXPROCS(0)
	}
	if workers < 1 {
		workers = 1
	}
	// Every file counts as one unit whether its metadata read succeeds or not: the
	// bar measures the work done, and a file skipped for being unreadable is work.
	bar := prog.Begin(PhaseEXIF, len(found))
	defer bar.Close()

	sem := make(chan struct{}, workers)
	var (
		wg         sync.WaitGroup
		mu         sync.Mutex
		photos     = make([]photo.Photo, 0, len(found))
		unreadable int
	)
	for _, f := range found {
		// Checked before the semaphore, which blocks: the same shape organize.execute
		// uses, so a cancellation costs at most the files already in flight.
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(f scan.Found) {
			defer wg.Done()
			defer func() { <-sem }()
			defer bar.Inc()
			p, err := exifmeta.Read(f.Path, f.Format)
			switch {
			case errors.Is(err, exifmeta.ErrEXIFPanic):
				// The parser crashed, not the file: the photo is still there and still
				// datable from its name or its mtime, so keep it and name it in the logs.
				// Counting it as unreadable would drop a file the run was asked to copy.
				logger.Warn("exif parser crashed on this file: dating it by name or mtime instead",
					"file", f.Path, "err", err)
			case err != nil:
				logger.Warn("file skipped", "file", f.Path, "err", err)
				mu.Lock()
				unreadable++
				mu.Unlock()
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
	return photos, unreadable
}
