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

// gapDurations are common --gap values. The flag accepts any Go duration; these
// are suggestions, so the list stays short rather than exhaustive.
var gapDurations = []string{"30m", "1h", "6h", "12h", "24h"}

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

// registerSharedCompletions wires the completions common to sort and clean: the
// positional source argument, --dest (directories only) and --log-level.
func registerSharedCompletions(cmd *cobra.Command) {
	cmd.ValidArgsFunction = completeSource
	_ = cmd.MarkFlagDirname("dest")
	_ = cmd.RegisterFlagCompletionFunc("log-level", completeFixed(logLevels...))
}
