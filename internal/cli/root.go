package cli

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/sgaunet/moraine/internal/config"
)

// newRootCmd builds the moraine root command and attaches every subcommand. The
// root has no Run of its own: invoked bare it prints help (exit 0). Errors and
// usage are silenced so cli.Execute owns all error rendering and the exit-code
// mapping; the --version flag is enabled here (mirrors the `version` subcommand).
//
// The values of the persistent --output and --config flags are shared with the
// subcommands through pointers: cobra parses into them before any RunE runs, so each
// command reads the resolved value at execution time.
func newRootCmd(version string, stdout, stderr io.Writer) *cobra.Command {
	var output, configPath string
	// One resolution for both spellings: --version prints this report's first line,
	// `version` prints the whole thing.
	build := buildReport(version)
	root := &cobra.Command{
		Use:   "moraine",
		Short: "Automatic photo organizer",
		Long: `moraine — automatic photo organizer.

Analyzes the photos in a directory (or a single photo), groups them into events by
capture time, assigns a theme to each group, then COPIES each photo to
destination/<theme>/<year>/<year-month-day>/<name> (see sort --path-template to
change that layout). Originals are never modified or deleted.

Commands:
  sort      organize photos into dated, themed folders
  clean     delete source originals already copied to the destination
  undo      remove the copies made by the last sort run
  version   print the version
  completion  generate the shell completion script

Output:
  stdout carries the run result only, as one key=value line (--output=text, the
  default) or one JSON object (--output=json). Logs, progress and errors go to
  stderr, so moraine is safe on either side of a pipe.

Configuration file:
  Settings may be kept in ~/.config/moraine.yaml (or $XDG_CONFIG_HOME/moraine.yaml,
  or the file named by --config or $MORAINE_CONFIG). A command-line flag always beats
  the file, and the file always beats the built-in default. Mode flags — --dry-run,
  --delete and --incremental — are deliberately not configurable: they choose what a
  single invocation does. Set MORAINE_CONFIG= (empty) to ignore the file entirely.

Exit codes:
  0  success
  1  runtime failure (unreadable source, missing exiftool, interrupted run)
  2  usage error (unknown command or flag, bad argument count or value)

Run "moraine <command> --help" for command-specific options and examples.`,
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       build.Version,
	}
	root.SetVersionTemplate("moraine {{.Version}}\n")

	// --output is persistent: every command writes its result to stdout, so the
	// rendering choice belongs to the tool rather than to each subcommand.
	root.PersistentFlags().StringVar(&output, "output", config.DefaultOutput,
		"stdout format for the run result: text|json (logs always go to stderr)")
	_ = root.RegisterFlagCompletionFunc("output", completeFixed(outputFormats...))

	// --config is persistent for the same reason: the file describes the tool, not one
	// subcommand. Named explicitly, a missing file is an error; the default locations
	// are optional, since most runs have no configuration file at all.
	root.PersistentFlags().StringVar(&configPath, "config", "",
		"read settings from this YAML file instead of the default locations "+
			"($MORAINE_CONFIG, $XDG_CONFIG_HOME/moraine.yaml, ~/.config/moraine.yaml); "+
			"command-line flags always win")

	root.AddCommand(newSortCmd(stdout, stderr, &output, &configPath))
	root.AddCommand(newCleanCmd(stdout, stderr, &output, &configPath))
	root.AddCommand(newUndoCmd(stdout, stderr, &output, &configPath))
	root.AddCommand(newVersionCmd(build, stdout, &output))

	return root
}
