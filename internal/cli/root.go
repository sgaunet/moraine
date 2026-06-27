package cli

import (
	"io"

	"github.com/spf13/cobra"
)

// newRootCmd builds the moraine root command and attaches every subcommand. The
// root has no Run of its own: invoked bare it prints help (exit 0). Errors and
// usage are silenced so cli.Execute owns all error rendering and the exit-code
// mapping; the --version flag is enabled here (mirrors the `version` subcommand).
func newRootCmd(version string, stdout io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:   "moraine",
		Short: "Automatic photo organizer",
		Long: `moraine — automatic photo organizer.

Analyzes the photos in a directory (or a single photo), groups them into events by
capture time, assigns a theme to each group, then COPIES each photo to
destination/<theme>/<year>/<year-month-day>/<name>. Originals are never modified or
deleted.

Commands:
  sort      organize photos into dated, themed folders
  clean     delete source originals already copied to the destination
  version   print the version

Run "moraine <command> --help" for command-specific options and examples.`,
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       version,
	}
	root.SetVersionTemplate("moraine {{.Version}}\n")

	root.AddCommand(newSortCmd(stdout))
	root.AddCommand(newCleanCmd(stdout))
	root.AddCommand(newVersionCmd(version, stdout))

	return root
}
