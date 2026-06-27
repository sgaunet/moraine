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
)

// newCleanCmd builds the `clean` subcommand: it deletes source originals that have a
// byte-identical copy under the destination. Dry-run by default; --delete commits.
// config.NewClean errors are usage errors (exit 2); Validate and app.Clean are
// runtime errors (exit 1). It needs neither exiftool nor the classifier.
func newCleanCmd(stdout io.Writer) *cobra.Command {
	var opts config.CleanOptions
	cmd := &cobra.Command{
		Use:   "clean [flags] <source-dir>",
		Short: "Delete source originals already copied to the destination",
		Long: `Recursively match each source file against the destination by SHA-256 content
(never by filename) and delete a source original only when a byte-identical copy
exists under the destination. Non-photo files and anything not safely copied are
left untouched.

Safety:
  - Dry-run by default; --delete is required to remove anything.
  - Files under the destination tree are never deleted (even nested inside source).
  - On any read/hash/permission error, the original is kept (fail-safe).
  - Only regular files are considered; symlinks and special files are skipped.`,
		Example: `  # preview what would be removed (deletes nothing)
  moraine clean --dest ~/Photos/sorted ~/Photos/2025

  # after reviewing, actually delete the copied originals
  moraine clean --delete -d ~/Photos/sorted ~/Photos/2025`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			opts.Source = args[0]

			cfg, err := config.NewClean(opts)
			if err != nil {
				return err // cross-field/syntax error → usage (exit 2)
			}
			if err := cfg.Validate(); err != nil {
				return asRuntime(err)
			}

			logger := slog.New(slog.NewTextHandler(stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			if _, err := app.Clean(ctx, cfg, logger); err != nil {
				return asRuntime(err)
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVarP(&opts.Dest, "dest", "d", "", "destination root holding the copies (default <source>/_sorted; never deleted from)")
	f.BoolVar(&opts.Delete, "delete", false, "actually delete matched originals (default: dry-run, deletes nothing)")
	f.StringVarP(&opts.LogLevel, "log-level", "l", config.DefaultLogLevel, "log verbosity: debug|info|warn|error")

	return cmd
}
