package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/sgaunet/moraine/internal/app"
	"github.com/sgaunet/moraine/internal/config"
	"github.com/sgaunet/moraine/internal/rawpreview"
)

// newSortCmd builds the `sort` subcommand: the photo-organizing pipeline. Flag
// values bind into a config.Options; RunE turns them into a validated config.Config
// (config.New errors are usage errors → exit 2) and runs the pipeline (filesystem
// validation, the exiftool preflight, and app.Organize are runtime errors → exit 1).
func newSortCmd(stdout, stderr io.Writer, output *string) *cobra.Command {
	var opts config.Options
	cmd := &cobra.Command{
		Use:   "sort [flags] <directory-or-file>",
		Short: "Organize photos into dated, themed folders",
		Long: `Organize photos: scan a directory (or a single file), group photos into events by
capture time, assign a theme to each group, then COPY each photo to
destination/<theme>/<year>/<year-month-day>/<name>. Originals are never modified.

The layout below the destination root is set by --path-template, built from the
placeholders {theme} {year} {month} {day} {date} separated by "/" — the default
"{theme}/{year}/{date}" is the layout above. Literal text is allowed between
placeholders. A template may omit {theme} (events of different themes then share a
folder, resolved by the usual skip-identical/" (N)" rules); it may not be absolute,
contain "." or ".." segments, or start with ".moraine", which is reserved.

Dating (a date is always assigned):
  1. the EXIF capture date;
  2. otherwise a date encoded in the file name (IMG_20230815_120000.jpg,
     IMG-20230815-WA0001.jpg, "Screenshot 2023-08-15 at 12.00.00.png") — this keeps
     a batch of downloads, which share one modification time, from collapsing into a
     single event dated by the day they were downloaded;
  3. otherwise the file's modification time.
A photo left with no usable date at all is filed under <theme>/unknown-date/: the
date-derived part of the template collapses to a single "unknown-date" segment, so
"{year}/{month}/{theme}" gives unknown-date/<theme>/ rather than a folder that looks
like a real date.

Scanning never follows a symlink as a directory: a symlinked folder under the source
is not descended into (reported with --verbose), while a symlinked file whose name
has a recognised extension is read and copied like any other photo. The destination
is identified by the directory itself, so naming it through a symlink or a different
letter case still excludes it from the scan.

Classification (a theme is always assigned):
  1. if --sample > 0: the Ollama vision model picks among the themes (a group of
     <= 3 photos is sent whole, otherwise a sample of --sample photos);
  2. otherwise, or on failure/out-of-list answer: the altitude heuristic labels a
     group with an EXIF altitude >= --mountain-altitude (default 1500 m) as
     "mountain", when "mountain" is one of the themes;
  3. otherwise: the fallback theme (--fallback-theme).
The model also reports how confident it is. --min-confidence rejects a verdict below
a threshold, sending that group to the heuristic and then the fallback; it defaults
to 0, which accepts every verdict. --vote classifies each sampled photo of a large
group separately and lets them vote, which costs one model call per sampled photo
but detects a mixed event: the share of votes the winning theme takes becomes its
confidence, and a tie abstains.
RAW photos (.dng/.nef/.cr2/...) are classified from the embedded preview exiftool
extracts (exiftool is required, see --exiftool). HEIC embeds no such preview, so it
is decoded by the first of sips, heif-convert, ffmpeg or magick found on PATH; that
converter is OPTIONAL, and without one HEIC photos are still dated, organized and
copied, only their group falls back to the heuristic or the fallback theme.
Images are downscaled before being sent, and a RAW or HEIC shot alongside its own
JPEG is sent only once.

Output:
  stdout carries the run summary only (--output=text, the default) or the full
  result with one record per file (--output=json). Logs go to stderr.

Exit codes:
  0  success
  1  runtime failure (unreadable source, missing exiftool, interrupted run)
  2  usage error (unknown flag, bad argument count or value)`,
		Example: `  # organize a directory
  moraine sort --dest ~/Photos/sorted ~/Photos/2025

  # a single photo (short flags)
  moraine sort -d ~/Photos/sorted ~/Photos/2025/IMG_1234.jpg

  # without Ollama (heuristic + fallback only)
  moraine sort -s 0 -d ~/Photos/sorted ~/Photos/2025

  # preview the plan without writing anything
  moraine sort --dry-run -d ~/Photos/sorted ~/Photos/2025

  # machine-readable result (logs discarded)
  moraine sort --output=json -d ~/Photos/sorted ~/Photos/2025 2>/dev/null

  # photos only — do not copy companion/sidecar files
  moraine sort --sidecars=false -d ~/Photos/sorted ~/Photos/2025

  # per-photo majority vote, and only trust a group the model is sure about
  moraine sort --vote --min-confidence 0.7 -d ~/Photos/sorted ~/Photos/2025

  # custom vocabulary + verbose logs
  moraine sort --themes "friends,hiking,party,nature" --fallback-theme misc \
    -l debug -d ~/Photos/sorted ~/Photos/2025`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			opts.Source = args[0]
			opts.Output = *output

			cfg, err := config.New(opts)
			if err != nil {
				return err // cross-field/syntax error → usage (exit 2)
			}
			if err := cfg.Validate(); err != nil {
				return asRuntime(err)
			}
			// exiftool is mandatory (RAW support): verify it before scanning,
			// classifying, or copying any file so a missing dependency fails fast.
			if err := rawpreview.EnsureAvailable(cfg.ExifToolPath); err != nil {
				return asRuntime(err)
			}

			logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			rep := newReporter(cfg.Output, stdout)
			sum, runErr := app.Organize(ctx, cfg, logger, rep.addSort)

			// Report before deciding the exit code: an interrupted run still did
			// real work, and the tally is the only record of what it managed.
			interrupted := isInterrupt(runErr)
			if err := rep.emitSort(cfg, sum, interrupted); err != nil {
				return asRuntime(err)
			}
			if interrupted {
				return asRuntime(fmt.Errorf(
					"interrupted: copied %d, skipped %d, renamed %d, errors %d",
					sum.Copied, sum.Skipped, sum.Renamed, sum.Errors))
			}
			return asRuntime(runErr)
		},
	}

	f := cmd.Flags()
	f.StringVarP(&opts.Dest, "dest", "d", "", "destination root (default <source>/_sorted; excluded from the scan)")
	f.DurationVarP(&opts.Gap, "gap", "g", config.DefaultGap, "max time gap within an event (e.g. 30m, 2h)")
	f.IntVarP(&opts.Sample, "sample", "s", config.DefaultSample, "photos sampled per large group (0 disables the model)")
	f.StringVar(&opts.Model, "model", config.DefaultModel, "Ollama vision model")
	f.StringVar(&opts.Themes, "themes", config.DefaultThemes, "themes ([a-z0-9-] slugs, comma-separated)")
	f.StringVar(&opts.PathTemplate, "path-template", config.DefaultPathTemplate,
		"destination layout below the root, from {theme} {year} {month} {day} {date} "+
			"(e.g. \"{year}/{month}\"); an undated event's date part becomes \"unknown-date\"")
	f.StringVar(&opts.OllamaURL, "ollama-url", config.DefaultOllamaURL, "base URL of the local Ollama API")
	f.StringVar(&opts.Fallback, "fallback-theme", config.DefaultFallback, "fallback theme when none is determined")
	f.StringVarP(&opts.LogLevel, "log-level", "l", config.DefaultLogLevel, "log verbosity: debug|info|warn|error")
	f.StringVar(&opts.ExifTool, "exiftool", config.DefaultExifTool, "exiftool executable (name on PATH or absolute path); required to read RAW files")
	f.BoolVar(&opts.Sidecars, "sidecars", true, "also copy companion/sidecar files next to each photo (e.g. IMG.jpg.xmp, IMG.xmp); --sidecars=false to disable")
	f.BoolVar(&opts.Incremental, "incremental", false,
		"skip photos the run manifest already records as copied (matches on size and modification time "+
			"instead of comparing bytes, and reuses each known event's theme)")
	f.BoolVarP(&opts.DryRun, "dry-run", "n", false, "report what would be copied, skipped or renamed without writing anything")
	f.IntVarP(&opts.Jobs, "jobs", "j", 0, "EXIF reader workers (0 = one per CPU); lower it to throttle a network drive")
	f.Float64Var(&opts.MountainAltitude, "mountain-altitude", config.DefaultMountainAltitude,
		"metres at/above which the altitude heuristic labels a group \"mountain\" (must be > 0)")
	f.Float64Var(&opts.MinConfidence, "min-confidence", 0,
		"reject a model verdict below this confidence, 0..1 (0 = accept every verdict)")
	f.BoolVar(&opts.Vote, "vote", false,
		"classify each sampled photo of a large group separately and take the majority "+
			"(one model call per sampled photo; the vote margin becomes the confidence)")

	registerVerbosityFlags(cmd, &opts.Quiet, &opts.Verbose)
	registerSharedCompletions(cmd)
	_ = cmd.RegisterFlagCompletionFunc("jobs", completeFixed())
	_ = cmd.RegisterFlagCompletionFunc("themes", completeThemeList)
	_ = cmd.RegisterFlagCompletionFunc("path-template", completeFixed(pathTemplates...))
	_ = cmd.RegisterFlagCompletionFunc("fallback-theme", completeFixed(append(defaultThemes(), config.DefaultFallback)...))
	_ = cmd.RegisterFlagCompletionFunc("gap", completeFixed(gapDurations...))
	_ = cmd.RegisterFlagCompletionFunc("mountain-altitude", completeFixed(altitudeMetres...))
	_ = cmd.RegisterFlagCompletionFunc("min-confidence", completeFixed(confidenceThresholds...))
	// Scalar flags with no knowable value set: suppress the filename fallback.
	_ = cmd.RegisterFlagCompletionFunc("sample", completeFixed())
	_ = cmd.RegisterFlagCompletionFunc("model", completeFixed(config.DefaultModel))
	_ = cmd.RegisterFlagCompletionFunc("ollama-url", completeFixed(config.DefaultOllamaURL))

	return cmd
}
