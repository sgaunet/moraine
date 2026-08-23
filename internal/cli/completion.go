package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/sgaunet/moraine/internal/config"
	"github.com/sgaunet/moraine/internal/photo"
)

// completionFunc is the shape cobra expects for dynamic completion: it receives
// the command, the positional args already typed, and the word being completed.
type completionFunc func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective)

// logLevels are the canonical values accepted by --log-level. config.parseLevel
// also accepts "warning", deliberately not offered here: it is an alias, not a
// canonical value.
var logLevels = []string{"debug", "info", "warn", "error"}

// outputFormats are the values accepted by --output, kept in step with the
// config.OutputFormat constants.
var outputFormats = []string{string(config.OutputText), string(config.OutputJSON)}

// gapDurations are common --gap values. The flag accepts any Go duration; these
// are suggestions, so the list stays short rather than exhaustive.
var gapDurations = []string{"30m", "1h", "6h", "12h", "24h"}

// confidenceThresholds are common --min-confidence values. 0 is offered first
// because it is the default and the way to turn the gate back off.
var confidenceThresholds = []string{"0", "0.5", "0.6", "0.7", "0.8", "0.9"}

// altitudeMetres are common --mountain-altitude values. The flag accepts any
// positive number; these are suggestions, so the list stays short.
var altitudeMetres = []string{"800", "1000", "1500", "2000", "2500"}

// defaultThemes returns the built-in theme slugs, derived from
// config.DefaultThemes so the completion candidates cannot drift from the flag
// default.
func defaultThemes() []string {
	return strings.Split(config.DefaultThemes, ",")
}

// completeFixed offers a fixed value set and suppresses filename fallback. Called
// with no values it only suppresses the fallback, which is what non-path scalar
// flags (--sample, --model, --ollama-url) want.
func completeFixed(values ...string) completionFunc {
	return func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return values, cobra.ShellCompDirectiveNoFileComp
	}
}

// completeThemeList completes --themes, whose value is a comma-separated list. It
// suggests the remaining built-in themes for the element being typed, preserving
// the comma-separated prefix already on the line, and asks the shell not to append
// a space so the user can keep typing ",<next>".
func completeThemeList(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	prefix, current := splitLastElement(toComplete)
	themes := defaultThemes()
	chosen := make(map[string]bool, len(themes))
	for theme := range strings.SplitSeq(prefix, ",") {
		chosen[theme] = true
	}

	out := make([]string, 0, len(themes))
	for _, theme := range themes {
		if chosen[theme] || !strings.HasPrefix(theme, current) {
			continue
		}
		out = append(out, prefix+theme)
	}
	if len(out) == 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return out, cobra.ShellCompDirectiveNoSpace | cobra.ShellCompDirectiveNoFileComp
}

// splitLastElement splits a comma-separated value into everything up to and
// including the final comma, and the trailing element being typed.
func splitLastElement(value string) (prefix, current string) {
	i := strings.LastIndex(value, ",")
	if i < 0 {
		return "", value
	}
	return value[:i+1], value[i+1:]
}

// completeSource completes the single positional argument of sort/clean, which is
// a directory or a single photo. Cobra's FilterFileExt directive keeps
// directories browsable while narrowing files to the extensions moraine reads.
func completeSource(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp // takes exactly one argument
	}
	exts := photo.Extensions()
	out := make([]string, 0, len(exts))
	for _, ext := range exts {
		out = append(out, strings.TrimPrefix(ext, "."))
	}
	return out, cobra.ShellCompDirectiveFilterFileExt
}

// completeDestRoot completes undo's single positional argument, which is a
// destination root: directories only, and only while none has been given.
func completeDestRoot(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp // takes exactly one argument
	}
	return nil, cobra.ShellCompDirectiveFilterDirs
}

// registerSharedCompletions wires the completions common to sort and clean: the
// positional source argument, --dest (directories only) and --log-level.
func registerSharedCompletions(cmd *cobra.Command) {
	cmd.ValidArgsFunction = completeSource
	_ = cmd.MarkFlagDirname("dest")
	_ = cmd.RegisterFlagCompletionFunc("log-level", completeFixed(logLevels...))
}

// registerVerbosityFlags adds --quiet/--verbose as the two shorthands over
// --log-level and declares all three mutually exclusive. Letting cobra enforce that
// keeps the rule in one line and surfaces a violation as a usage error (exit 2),
// instead of plumbing "was --log-level set?" down into the config package.
func registerVerbosityFlags(cmd *cobra.Command, quiet, verbose *bool) {
	f := cmd.Flags()
	f.BoolVarP(quiet, "quiet", "q", false, "log errors only (same as --log-level error)")
	f.BoolVarP(verbose, "verbose", "v", false, "log every file (same as --log-level debug)")
	cmd.MarkFlagsMutuallyExclusive("quiet", "verbose", "log-level")
}
