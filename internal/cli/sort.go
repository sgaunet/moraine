package cli

import (
	"context"
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
func newSortCmd(stdout io.Writer) *cobra.Command {
	var opts config.Options
	cmd := &cobra.Command{
		Use:   "sort [flags] <directory-or-file>",
		Short: "Organize photos into dated, themed folders",
		Long: `Organize photos: scan a directory (or a single file), group photos into events by
capture time, assign a theme to each group, then COPY each photo to
destination/<theme>/<year>/<year-month-day>/<name>. Originals are never modified.

Classification (a theme is always assigned):
  1. heuristic: EXIF altitude >= 1500 m -> "mountain" (no model call);
  2. otherwise, if --sample > 0: the Ollama vision model picks among the themes
     (a group of <= 3 photos is sent whole, otherwise a sample of --sample photos);
  3. otherwise, or on failure/out-of-list answer: the fallback theme (--fallback-theme).
HEIC photos are dated and organized but not sent to the model. RAW photos
(.dng/.nef/.cr2/...) are organized too; their embedded preview is extracted with
exiftool (required, see --exiftool) and sent to the model.`,
		Example: `  # organize a directory
  moraine sort --dest ~/Photos/sorted ~/Photos/2025

  # a single photo (short flags)
  moraine sort -d ~/Photos/sorted ~/Photos/2025/IMG_1234.jpg

  # without Ollama (heuristic + fallback only)
  moraine sort -s 0 -d ~/Photos/sorted ~/Photos/2025

  # photos only — do not copy companion/sidecar files
  moraine sort --sidecars=false -d ~/Photos/sorted ~/Photos/2025

  # custom vocabulary + verbose logs
  moraine sort --themes "friends,hiking,party,nature" --fallback-theme misc \
    -l debug -d ~/Photos/sorted ~/Photos/2025`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			opts.Source = args[0]

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

			logger := slog.New(slog.NewTextHandler(stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			if _, err := app.Organize(ctx, cfg, logger); err != nil {
				return asRuntime(err)
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVarP(&opts.Dest, "dest", "d", "", "destination root (default <source>/_sorted; excluded from the scan)")
	f.DurationVarP(&opts.Gap, "gap", "g", config.DefaultGap, "max time gap within an event (e.g. 30m, 2h)")
	f.IntVarP(&opts.Sample, "sample", "s", config.DefaultSample, "photos sampled per large group (0 disables the model)")
	f.StringVar(&opts.Model, "model", config.DefaultModel, "Ollama vision model")
	f.StringVar(&opts.Themes, "themes", config.DefaultThemes, "themes ([a-z0-9-] slugs, comma-separated)")
	f.StringVar(&opts.OllamaURL, "ollama-url", config.DefaultOllamaURL, "base URL of the local Ollama API")
	f.StringVar(&opts.Fallback, "fallback-theme", config.DefaultFallback, "fallback theme when none is determined")
	f.StringVarP(&opts.LogLevel, "log-level", "l", config.DefaultLogLevel, "log verbosity: debug|info|warn|error")
	f.StringVar(&opts.ExifTool, "exiftool", config.DefaultExifTool, "exiftool executable (name on PATH or absolute path); required to read RAW files")
	f.BoolVar(&opts.Sidecars, "sidecars", true, "also copy companion/sidecar files next to each photo (e.g. IMG.jpg.xmp, IMG.xmp); --sidecars=false to disable")

	return cmd
}
