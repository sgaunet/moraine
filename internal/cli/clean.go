package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/sgaunet/moraine/internal/app"
	"github.com/sgaunet/moraine/internal/config"
	"github.com/sgaunet/moraine/internal/configfile"
)

// newCleanCmd builds the `clean` subcommand: it deletes source originals that have a
// byte-identical copy under the destination. Dry-run by default; --delete commits.
// config.NewClean errors are usage errors (exit 2); Validate and app.Clean are
// runtime errors (exit 1). It needs neither exiftool nor the classifier.
func newCleanCmd(stdout, stderr io.Writer, output, progress, configPath *string) *cobra.Command {
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
  - Only regular files are considered; symlinks and special files are skipped.

Output:
  stdout carries the run summary only (--output=text, the default) or the full
  plan with one record per evaluated file (--output=json). The per-file decisions
  are logged to stderr, where they stay readable next to a redirected stdout.

Exit codes:
  0  success
  1  runtime failure (unreadable source or destination, interrupted run)
  2  usage error (unknown flag, bad argument count or value)`,
		Example: `  # preview what would be removed (deletes nothing)
  moraine clean --dest ~/Photos/sorted ~/Photos/2025

  # after reviewing, actually delete the copied originals
  moraine clean --delete -d ~/Photos/sorted ~/Photos/2025

  # machine-readable plan (logs discarded)
  moraine clean --output=json -d ~/Photos/sorted ~/Photos/2025 2>/dev/null`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Source = args[0]
			opts.Output = *output
			opts.Progress = *progress

			file, from, err := configfile.Load(*configPath)
			if err != nil {
				return err
			}
			fromFile := applyCleanFile(cmd, &opts, file)

			cfg, err := config.NewClean(opts)
			if err != nil {
				return fileHint(err, from, fromFile) // cross-field/syntax error → usage (exit 2)
			}
			if err := cfg.Validate(); err != nil {
				return asRuntime(err)
			}

			logger, prog := newRenderer(cfg.Progress, stdout, stderr, cfg.LogLevel, !cfg.Delete)
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			rep := newReporter(cfg.Output, stdout)
			sum, runErr := app.Clean(ctx, cfg, logger, rep.addClean, prog)

			// Report before deciding the exit code: an interrupted run may already
			// have deleted originals, and the tally is the only record of which.
			interrupted := isInterrupt(runErr)
			if err := rep.emitClean(cfg, sum, interrupted); err != nil {
				return asRuntime(err)
			}
			if interrupted {
				return asRuntime(fmt.Errorf(
					"interrupted: deleted %d, would-delete %d, kept %d, errors %d",
					sum.Deleted, sum.WouldDelete, sum.Kept, sum.Errors))
			}
			return asRuntime(runErr)
		},
	}

	registerCleanFlags(cmd.Flags(), &opts)

	registerVerbosityFlags(cmd, &opts.Quiet, &opts.Verbose)
	registerSharedCompletions(cmd)

	return cmd
}

// registerCleanFlags declares every value flag of `clean` on f, bound to opts. See
// registerSortFlags for why the registration is reachable on its own.
func registerCleanFlags(f *pflag.FlagSet, opts *config.CleanOptions) {
	f.StringVarP(&opts.Dest, "dest", "d", "", "destination root holding the copies (default <source>/_sorted; never deleted from)")
	f.BoolVar(&opts.Delete, "delete", false, "actually delete matched originals (default: dry-run, deletes nothing)")
	f.StringVarP(&opts.LogLevel, "log-level", "l", config.DefaultLogLevel, "log verbosity: debug|info|warn|error")
}
