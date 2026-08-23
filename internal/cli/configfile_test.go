package cli_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sgaunet/moraine/internal/cli"
)

// writeConfig puts a configuration file in a fresh temp dir and returns its path.
func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "moraine.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The file supplies what the command line left out — the whole point of the feature.
// Both settings here are observable from outside: output changes stdout's shape, and
// path_template changes where the photo lands.
func TestConfigFileSuppliesSettingsTheCommandLineOmits(t *testing.T) {
	cfgPath := writeConfig(t, `
output: json
sort:
  path_template: "{year}/{month}"
`)
	args, dest := sortFixture(t, "--config", cfgPath)
	var out bytes.Buffer
	if code := cli.Execute("dev", args, &out, io.Discard); code != 0 {
		t.Fatalf("exit = %d", code)
	}

	// output: json took effect, so stdout parses as a document rather than a line.
	var doc struct {
		Command string `json:"command"`
		Summary struct {
			Copied int `json:"copied"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("the file's output: json did not take effect; stdout was:\n%s", out.String())
	}
	if doc.Command != "sort" || doc.Summary.Copied != 1 {
		t.Errorf("unexpected document: %+v", doc)
	}

	// path_template took effect: no theme folder, a year/month path instead.
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 { // the year folder plus .moraine
		t.Fatalf("destination entries = %v, want a year folder and .moraine", names(entries))
	}
	var years []string
	for _, e := range entries {
		if e.Name() != ".moraine" {
			years = append(years, e.Name())
		}
	}
	if len(years) != 1 || len(years[0]) != 4 {
		t.Errorf("want a single four-digit year folder, got %v", years)
	}
}

func names(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// A typed flag always beats the file. sortFixture already passes --sample 0, so a
// file that says otherwise must lose — including when the flag's value happens to
// equal its own default, which is why this is decided by cobra's Changed and not by
// comparing values.
func TestFlagBeatsTheConfigFile(t *testing.T) {
	cfgPath := writeConfig(t, "output: json\nsort:\n  sample: 3\n")
	// --output=text on the command line must beat output: json in the file.
	args, _ := sortFixture(t, "--config", cfgPath, "--output=text")
	var out bytes.Buffer
	if code := cli.Execute("dev", args, &out, io.Discard); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.HasPrefix(strings.TrimSpace(out.String()), "scanned=") {
		t.Errorf("--output=text must beat the file's json; got:\n%s", out.String())
	}
}

// The file must not be able to flip a mode flag. Principle V exists so a mistyped
// command cannot delete anything; a file that made clean destructive by default, or
// every sort a silent no-op, would subvert that. Strict decoding turns each of these
// into a plain rejection.
func TestConfigFileCannotSetAModeFlag(t *testing.T) {
	for _, contents := range []string{
		"sort:\n  dry_run: true\n",
		"sort:\n  incremental: true\n",
		"clean:\n  delete: true\n",
		"undo:\n  delete: true\n",
		"sort:\n  quiet: true\n",
		"sort:\n  move: true\n",
	} {
		cfgPath := writeConfig(t, contents)
		args, _ := sortFixture(t, "--config", cfgPath)
		var errb bytes.Buffer
		if code := cli.Execute("dev", args, io.Discard, &errb); code != 2 {
			t.Errorf("%q: exit = %d, want 2 (usage)", contents, code)
		}
	}
}

func TestConfigFileErrorsAreUsageErrors(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		wantIn   string
	}{
		{"unknown key", "sort:\n  gapp: 6h\n", "gapp"},
		{"malformed yaml", "sort:\n\tgap: 6h\n", "moraine.yaml"},
		{"bad duration", "sort:\n  gap: nonsense\n", "nonsense"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfgPath := writeConfig(t, tc.contents)
			args, _ := sortFixture(t, "--config", cfgPath)
			var errb bytes.Buffer
			if code := cli.Execute("dev", args, io.Discard, &errb); code != 2 {
				t.Fatalf("exit = %d, want 2 (usage); stderr=%s", code, errb.String())
			}
			// The message must point at the file, not at a flag the user never typed.
			if !strings.Contains(errb.String(), cfgPath) {
				t.Errorf("the error must name the config file %q; got:\n%s", cfgPath, errb.String())
			}
			if !strings.Contains(errb.String(), tc.wantIn) {
				t.Errorf("the error must mention %q; got:\n%s", tc.wantIn, errb.String())
			}
		})
	}
}

// A file named explicitly must exist: silently ignoring --config would hide a typo in
// the path and leave the user wondering why nothing applied.
func TestMissingExplicitConfigIsAUsageError(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "absent.yaml")
	args, _ := sortFixture(t, "--config", absent)
	var errb bytes.Buffer
	if code := cli.Execute("dev", args, io.Discard, &errb); code != 2 {
		t.Fatalf("exit = %d, want 2 (usage)", code)
	}
	if !strings.Contains(errb.String(), absent) {
		t.Errorf("the error must name the missing file; got:\n%s", errb.String())
	}
}

// A value that is well-formed YAML but invalid reaches config.New, whose messages are
// written in terms of flags. The hint is what stops "--gap must be strictly positive"
// from sending a user to look at a command line they never typed it on.
func TestBadValueInAConfigFileNamesItsSource(t *testing.T) {
	cfgPath := writeConfig(t, "sort:\n  gap: 0s\n")
	args, _ := sortFixture(t, "--config", cfgPath)
	var errb bytes.Buffer
	if code := cli.Execute("dev", args, io.Discard, &errb); code != 2 {
		t.Fatalf("exit = %d, want 2 (usage); stderr=%s", code, errb.String())
	}
	msg := errb.String()
	if !strings.Contains(msg, cfgPath) || !strings.Contains(msg, "gap") {
		t.Errorf("the error must name both the file and the setting; got:\n%s", msg)
	}
}

// themes reads as a YAML list, which is nicer to write than an embedded comma string,
// and must reach the same parser the flag feeds.
func TestConfigFileThemesAsAList(t *testing.T) {
	cfgPath := writeConfig(t, `
output: json
sort:
  themes: [hiking, party]
  fallback_theme: misc
`)
	args, _ := sortFixture(t, "--config", cfgPath)
	var out bytes.Buffer
	if code := cli.Execute("dev", args, &out, io.Discard); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var doc struct {
		Results []struct {
			Theme string `json:"theme"`
		} `json:"results"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out.String())
	}
	if len(doc.Results) == 0 {
		t.Fatal("no records")
	}
	// --sample 0 disables the model, so every group lands on the fallback theme —
	// here the one the file supplied.
	if doc.Results[0].Theme != "misc" {
		t.Errorf("theme = %q, want the file's fallback \"misc\"", doc.Results[0].Theme)
	}
}

// An invalid theme in the file is rejected by the same parser the flag uses.
func TestConfigFileRejectsInvalidThemes(t *testing.T) {
	cfgPath := writeConfig(t, "sort:\n  themes: [\"Not A Slug\"]\n")
	args, _ := sortFixture(t, "--config", cfgPath)
	var errb bytes.Buffer
	if code := cli.Execute("dev", args, io.Discard, &errb); code != 2 {
		t.Fatalf("exit = %d, want 2 (usage); stderr=%s", code, errb.String())
	}
}
