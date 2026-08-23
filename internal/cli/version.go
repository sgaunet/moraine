package cli

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/sgaunet/moraine/internal/config"
)

// newVersionCmd builds the `version` subcommand. It prints the build's identity —
// version, commit, build time, Go version and platform — read back from the binary
// itself (see buildReport), with no positional args, no filesystem access and no
// external-dependency access. Honours --output=json.
//
// The report is resolved once by newRootCmd and shared with the root --version flag,
// so the terse form is always exactly this command's first line.
func newVersionCmd(build versionReport, stdout io.Writer, output *string) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Long: `Print the moraine version and exit. Requires no source, destination, or external
tools. Also reports the commit, build time, Go version and platform when the binary
carries that stamp; --output=json renders the same fields as one JSON object.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			format, err := config.ParseOutput(*output)
			if err != nil {
				return err // invalid --output → usage (exit 2)
			}
			return asRuntime(build.emit(format, stdout))
		},
	}
}
