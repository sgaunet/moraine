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
	"github.com/spf13/pflag"

	"github.com/sgaunet/moraine/internal/app"
	"github.com/sgaunet/moraine/internal/config"
	"github.com/sgaunet/moraine/internal/configfile"
)

// newUndoCmd builds the `undo` subcommand: it removes the copies made by the most
// recent `sort` run, read from that run's manifest. Dry-run by default; --delete
// commits. config.NewUndo errors are usage errors (exit 2); Validate and app.Undo
// are runtime errors (exit 1). It needs neither exiftool nor the classifier.
func newUndoCmd(stdout, stderr io.Writer, output, configPath *string) *cobra.Command {
	var opts config.UndoOptions
	cmd := &cobra.Command{
		Use:   "undo [flags] <destination-root>",
		Short: "Remove the copies made by the last sort run",
		Long: `Reverse the most recent sort run, using the manifest that run wrote under the
destination. Only files that run created are removed, and only while they still
match the size and modification time recorded for them.

This is the mirror of "clean": clean deletes source ORIGINALS that are safely
archived, undo deletes the COPIES a run should not have made. Sources are never
read, moved or deleted by undo.

Safety:
  - Dry-run by default; --delete is required to remove anything.
  - A file the run only recognised (already identical in place) is never removed.
  - A copy edited or replaced since the run is kept, and reported as changed.
  - Nothing outside the destination root is ever touched.
  - Folders the removals empty are pruned; the destination root itself is kept.
  - After a successful --delete pass the manifest is kept as an audit trail and
    marked undone, so running undo again steps back to the run before it.

Output:
  stdout carries the run summary only (--output=text, the default) or the full
  plan with one record per evaluated file (--output=json). The per-file decisions
  are logged to stderr, where they stay readable next to a redirected stdout.

Exit codes:
  0  success
  1  runtime failure (unreadable destination, no manifest to undo, interrupted run)
  2  usage error (unknown flag, bad argument count or value)`,
		Example: `  # preview what the last run would give back (removes nothing)
  moraine undo ~/Photos/sorted

  # after reviewing, actually remove those copies
  moraine undo --delete ~/Photos/sorted

  # machine-readable plan (logs discarded)
  moraine undo --output=json ~/Photos/sorted 2>/dev/null`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Dest = args[0]
			opts.Output = *output

			file, from, err := configfile.Load(*configPath)
			if err != nil {
				return err
			}
			fromFile := applyUndoFile(cmd, &opts, file)

			cfg, err := config.NewUndo(opts)
			if err != nil {
				return fileHint(err, from, fromFile) // cross-field/syntax error → usage (exit 2)
			}
			if err := cfg.Validate(); err != nil {
				return asRuntime(err)
			}

			logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			rep := newReporter(cfg.Output, stdout)
			sum, runErr := app.Undo(ctx, cfg, logger, rep.addUndo)

			// Report before deciding the exit code: an interrupted undo may already
			// have removed copies, and the tally is the only record of which.
			interrupted := isInterrupt(runErr)
			if err := rep.emitUndo(cfg, sum, interrupted); err != nil {
				return asRuntime(err)
			}
			if interrupted {
				return asRuntime(fmt.Errorf(
					"interrupted: removed %d, kept %d, errors %d",
					sum.Removed, sum.Kept, sum.Errors))
			}
			return asRuntime(runErr)
		},
	}

	registerUndoFlags(cmd.Flags(), &opts)

	registerVerbosityFlags(cmd, &opts.Quiet, &opts.Verbose)
	cmd.ValidArgsFunction = completeDestRoot
	_ = cmd.RegisterFlagCompletionFunc("log-level", completeFixed(logLevels...))

	return cmd
}

// registerUndoFlags declares every value flag of `undo` on f, bound to opts. See
// registerSortFlags for why the registration is reachable on its own.
func registerUndoFlags(f *pflag.FlagSet, opts *config.UndoOptions) {
	f.BoolVar(&opts.Delete, "delete", false, "actually remove the recorded copies (default: dry-run, removes nothing)")
	f.StringVarP(&opts.LogLevel, "log-level", "l", config.DefaultLogLevel, "log verbosity: debug|info|warn|error")
}
