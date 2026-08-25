package cli

import (
	"io"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// ExecuteWithStdin runs the command tree with stdin as its standard input.
//
// It exists for the tests of `config edit`, which is the one command that reads a
// user rather than a file. Driving it through Execute would mean the process's real
// standard input — a terminal on a laptop, a closed descriptor in CI — so the tests
// need this one seam (Constitution Principle VII: export_test.go, never a test moved
// back inside the package).
func ExecuteWithStdin(version string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return execute(version, args, stdin, stdout, stderr)
}

// The seams below exist for configkeys_test.go, which keeps the table of settable
// values in step with the flags the real commands register. Both sides of that
// comparison are unexported, so the test needs a window onto each.

// Sections returns the sections of the configuration file.
func Sections() []string { return sections }

// SettingFlags returns the flags `moraine config set <section>` writes.
func SettingFlags(section string) []string {
	out := make([]string, 0, len(settingsFor(section)))
	for _, s := range settingsFor(section) {
		out = append(out, s.Flag)
	}
	return out
}

// SettingKeys returns the YAML keys of a section's settings.
func SettingKeys(section string) []string {
	out := make([]string, 0, len(settingsFor(section)))
	for _, s := range settingsFor(section) {
		out = append(out, s.YAML)
	}
	return out
}

// CommandFlags returns every flag the real command of a section registers.
func CommandFlags(section string) []string {
	var out []string
	var output, configPath string
	var cmd *cobra.Command
	switch section {
	case sectionClean:
		cmd = newCleanCmd(io.Discard, io.Discard, &output, &configPath)
	case sectionUndo:
		cmd = newUndoCmd(io.Discard, io.Discard, &output, &configPath)
	default:
		cmd = newSortCmd(io.Discard, io.Discard, &output, &configPath)
	}
	cmd.Flags().VisitAll(func(f *pflag.Flag) { out = append(out, f.Name) })
	return out
}

// UnconfigurableFlags returns the flags a configuration file deliberately cannot set.
func UnconfigurableFlags() []string { return unconfigurable }
