package cli_test

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/sgaunet/moraine/internal/cli"
	"github.com/sgaunet/moraine/internal/config"
	"github.com/sgaunet/moraine/internal/photo"
)

// completeOut runs cobra's hidden __complete command through cli.Execute and
// returns the exit code and the candidate block written to stdout. The trailing
// "Completion ended with directive" line goes to stderr, so it is discarded.
func completeOut(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := cli.Execute("dev", append([]string{"__complete"}, args...), &out, io.Discard)
	return code, out.String()
}

func TestCompletionScriptPerShell(t *testing.T) {
	tests := []struct {
		shell  string
		marker string
	}{
		{"bash", "bash completion V2 for moraine"},
		{"zsh", "#compdef moraine"},
		{"fish", "fish completion for moraine"},
		{"powershell", "powershell completion for moraine"},
	}
	for _, tc := range tests {
		t.Run(tc.shell, func(t *testing.T) {
			var out bytes.Buffer
			code := cli.Execute("dev", []string{"completion", tc.shell}, &out, io.Discard)
			if code != 0 {
				t.Fatalf("completion %s exit = %d, want 0", tc.shell, code)
			}
			if s := out.String(); !strings.Contains(s, tc.marker) {
				t.Errorf("completion %s missing %q; got first line:\n%s",
					tc.shell, tc.marker, strings.SplitN(s, "\n", 2)[0])
			}
		})
	}
}

// TestCompletionUnknownShellShowsHelp pins cobra's behaviour for an unsupported
// shell: `completion` is a parent command with one subcommand per shell and no
// Run of its own, so an unknown shell prints the completion help and exits 0 --
// the same as a bare `moraine`. It is not routed through the usage-error path.
func TestCompletionUnknownShellShowsHelp(t *testing.T) {
	var out bytes.Buffer
	code := cli.Execute("dev", []string{"completion", "tcsh"}, &out, io.Discard)
	if code != 0 {
		t.Errorf("completion tcsh exit = %d, want 0", code)
	}
	if s := out.String(); !strings.Contains(s, "Available Commands:") {
		t.Errorf("completion tcsh did not print the completion help; got:\n%s", s)
	}
}

func TestTopLevelHelpMentionsCompletion(t *testing.T) {
	var out bytes.Buffer
	cli.Execute("dev", []string{"--help"}, &out, io.Discard)
	if s := out.String(); !strings.Contains(s, "completion") {
		t.Errorf("top-level help does not mention completion; got:\n%s", s)
	}
}

// TestCompleteLogLevelOffersEveryAcceptedValue ties the completion candidates to
// the parser: every offered value must be accepted by config.New, so the two
// cannot drift.
func TestCompleteLogLevelOffersEveryAcceptedValue(t *testing.T) {
	for _, cmd := range []string{"sort", "clean"} {
		t.Run(cmd, func(t *testing.T) {
			code, out := completeOut(t, cmd, "--log-level", "")
			if code != 0 {
				t.Fatalf("exit = %d, want 0", code)
			}
			for _, level := range []string{"debug", "info", "warn", "error"} {
				if !strings.Contains(out, level) {
					t.Errorf("--log-level completion missing %q; got:\n%s", level, out)
				}
				opts := config.Options{
					Source:           ".",
					Gap:              config.DefaultGap,
					Sample:           config.DefaultSample,
					Themes:           config.DefaultThemes,
					Fallback:         config.DefaultFallback,
					LogLevel:         level,
					MountainAltitude: config.DefaultMountainAltitude,
				}
				if _, err := config.New(opts); err != nil {
					t.Errorf("completion offers %q but config.New rejects it: %v", level, err)
				}
			}
		})
	}
}

// TestCompleteSourceFiltersToScannedExtensions asserts the positional argument
// offers exactly the extensions the scanner recognises, derived from the same
// table (photo.Extensions) rather than a restated list.
func TestCompleteSourceFiltersToScannedExtensions(t *testing.T) {
	code, out := completeOut(t, "sort", "")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	for _, ext := range photo.Extensions() {
		if !strings.Contains(out, strings.TrimPrefix(ext, ".")) {
			t.Errorf("source completion missing extension %q; got:\n%s", ext, out)
		}
	}
	if !strings.Contains(out, ":8") {
		t.Errorf("want ShellCompDirectiveFilterFileExt (:8); got:\n%s", out)
	}
}

func TestCompleteDestIsDirectoriesOnly(t *testing.T) {
	for _, cmd := range []string{"sort", "clean"} {
		t.Run(cmd, func(t *testing.T) {
			code, out := completeOut(t, cmd, "--dest", "")
			if code != 0 {
				t.Fatalf("exit = %d, want 0", code)
			}
			if !strings.Contains(out, ":16") {
				t.Errorf("want ShellCompDirectiveFilterDirs (:16); got:\n%s", out)
			}
		})
	}
}

// TestCompleteThemesExtendsTheList checks the comma-separated behaviour: the
// typed prefix is preserved, an already-chosen theme is not offered again, and
// the shell is told not to append a space.
func TestCompleteThemesExtendsTheList(t *testing.T) {
	code, out := completeOut(t, "sort", "--themes", "mountain,")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "mountain,cook") {
		t.Errorf("want the typed prefix preserved (mountain,cook); got:\n%s", out)
	}
	if strings.Contains(out, "mountain,mountain") {
		t.Errorf("already-chosen theme offered again; got:\n%s", out)
	}
	if !strings.Contains(out, ":6") {
		t.Errorf("want NoSpace|NoFileComp (:6); got:\n%s", out)
	}
}

// TestCompleteExhaustedArityOffersNothing covers sort/clean taking exactly one
// argument: once it is given, no further candidates or filenames are offered.
func TestCompleteExhaustedArityOffersNothing(t *testing.T) {
	for _, cmd := range []string{"sort", "clean"} {
		t.Run(cmd, func(t *testing.T) {
			code, out := completeOut(t, cmd, t.TempDir(), "")
			if code != 0 {
				t.Fatalf("exit = %d, want 0", code)
			}
			if !strings.Contains(out, ":4") {
				t.Errorf("want ShellCompDirectiveNoFileComp (:4); got:\n%s", out)
			}
		})
	}
}
