package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// newVersionCmd builds the `version` subcommand. It prints the same string as the
// root --version flag ("moraine <version>") and exits 0, with no positional args,
// no flags, and no filesystem or external-dependency access.
func newVersionCmd(version string, stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Long:  "Print the moraine version and exit. Requires no source, destination, or external tools.",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			_, _ = fmt.Fprintln(stdout, "moraine", version)
			return nil
		},
	}
}
