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
	"github.com/sgaunet/moraine/internal/configfile"
)

// configAt points this test's moraine at a configuration file of its own and returns
// the path.
//
// It is load-bearing, not tidiness: TestMain sets MORAINE_CONFIG to the empty string,
// which turns the configuration file OFF for this package. A `config` test that forgot
// this would fail loudly rather than reach for — and rewrite — the real
// ~/.config/moraine.yaml of whoever is running the suite.
func configAt(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "moraine.yaml")
	if contents != "" {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(configfile.EnvVar, path)
	return path
}

// runConfig executes a config command and returns its stdout, stderr and exit code.
func runConfig(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errs bytes.Buffer
	code = cli.Execute("dev", append([]string{"config"}, args...), &out, &errs)
	return out.String(), errs.String(), code
}

// settings parses a --output=json config document into key → setting.
func settings(t *testing.T, stdout string) map[string]struct {
	Value   string `json:"value"`
	Origin  string `json:"origin"`
	Default string `json:"default"`
} {
	t.Helper()
	var doc struct {
		Settings []struct {
			Key     string `json:"key"`
			Value   string `json:"value"`
			Origin  string `json:"origin"`
			Default string `json:"default"`
		} `json:"settings"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout)
	}
	out := map[string]struct {
		Value   string `json:"value"`
		Origin  string `json:"origin"`
		Default string `json:"default"`
	}{}
	for _, s := range doc.Settings {
		out[s.Key] = struct {
			Value   string `json:"value"`
			Origin  string `json:"origin"`
			Default string `json:"default"`
		}{s.Value, s.Origin, s.Default}
	}
	return out
}

// The question `config show` exists to answer: which value will a run use, and did it
// come from the file or from the built-in default?
func TestConfigShowReportsTheEffectiveValueAndItsOrigin(t *testing.T) {
	configAt(t, "sort:\n  gap: 8h\n")
	stdout, _, code := runConfig(t, "show", "sort", "--output=json")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	got := settings(t, stdout)

	if s := got["sort.gap"]; s.Value != "8h" || s.Origin != "file" || s.Default != "6h" {
		t.Errorf("sort.gap = %+v, want the file's 8h over a default of 6h", s)
	}
	if s := got["sort.sample"]; s.Value != "3" || s.Origin != "default" {
		t.Errorf("sort.sample = %+v, want the default reported as such", s)
	}
}

// A top-level setting is inherited by every command, so that is the value each of
// them reports — anything else would answer a different question than "what will this
// run use?".
func TestConfigShowResolvesInheritanceFromTheTopLevel(t *testing.T) {
	configAt(t, "log_level: warn\nclean:\n  log_level: error\n")
	stdout, _, code := runConfig(t, "show", "--output=json")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	got := settings(t, stdout)

	for key, want := range map[string]string{
		"log_level":       "warn",  // the top level, as written
		"sort.log_level":  "warn",  // inherited
		"undo.log_level":  "warn",  // inherited
		"clean.log_level": "error", // overridden by the section
	} {
		if got[key].Value != want {
			t.Errorf("%s = %q, want %q", key, got[key].Value, want)
		}
		if got[key].Origin != "file" {
			t.Errorf("%s origin = %q, want file", key, got[key].Origin)
		}
	}
}

// Having no configuration file is the ordinary case, and must report every default
// rather than fail: `config show` is how a user finds out what the defaults are.
func TestConfigShowWithoutAFileReportsTheDefaults(t *testing.T) {
	configAt(t, "")
	stdout, _, code := runConfig(t, "show", "--output=json")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 when there is no configuration file", code)
	}
	var doc struct {
		Exists bool `json:"exists"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Exists {
		t.Error("exists = true for a file that is not there")
	}
	for key, s := range settings(t, stdout) {
		if s.Origin != "default" {
			t.Errorf("%s came from %q with no file to come from", key, s.Origin)
		}
	}
}

// The text rendering is the default one, so it is the one most scripts and eyes see.
func TestConfigShowTextRendering(t *testing.T) {
	configAt(t, "sort:\n  gap: 8h\n")

	stdout, _, code := runConfig(t, "show", "sort")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stdout, "sort.gap=8h origin=file\n") {
		t.Errorf("missing the origin-carrying line:\n%s", stdout)
	}

	bare, _, _ := runConfig(t, "show", "sort", "--origins=false")
	if !strings.Contains(bare, "sort.gap=8h\n") || strings.Contains(bare, "origin=") {
		t.Errorf("--origins=false should print bare key=value lines:\n%s", bare)
	}
}

// Naming a section limits the listing to it, which is what makes a 24-setting dump
// usable.
func TestConfigShowSection(t *testing.T) {
	configAt(t, "")
	stdout, _, code := runConfig(t, "show", "undo")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if strings.Contains(stdout, "sort.") {
		t.Errorf("`show undo` reported sort's settings:\n%s", stdout)
	}
	if !strings.Contains(stdout, "undo.log_level=") {
		t.Errorf("`show undo` reported nothing of undo:\n%s", stdout)
	}

	if _, _, code := runConfig(t, "show", "nonsense"); code != 2 {
		t.Errorf("an unknown section exit = %d, want 2", code)
	}
}

// Strict decoding is what makes a typo an error rather than a setting that silently
// does nothing, and `config show` must not be the one command that hides it.
func TestConfigShowRefusesAFileWithAnUnknownKey(t *testing.T) {
	configAt(t, "sort:\n  gpa: 8h\n")
	_, stderr, code := runConfig(t, "show")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "gpa") {
		t.Errorf("the error should name the offending key, got:\n%s", stderr)
	}
}

// "Why did my setting not apply?" is answered by naming the file and the rule that
// chose it.
func TestConfigPath(t *testing.T) {
	path := configAt(t, "")

	stdout, _, code := runConfig(t, "path")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stdout, "path="+path) || !strings.Contains(stdout, "exists=false") {
		t.Errorf("path = %q, want it to name %s as absent", stdout, path)
	}
	if !strings.Contains(stdout, "source="+configfile.EnvVar) {
		t.Errorf("the reason should be named, got %q", stdout)
	}

	if err := os.WriteFile(path, []byte("output: json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if stdout, _, _ = runConfig(t, "path"); !strings.Contains(stdout, "exists=true") {
		t.Errorf("path = %q, want exists=true", stdout)
	}
}

// A configuration file turned off has nowhere to write, which is a runtime condition
// rather than a mistyped argument.
func TestConfigPathRefusesWhenTheFileIsTurnedOff(t *testing.T) {
	t.Setenv(configfile.EnvVar, "")
	_, stderr, code := runConfig(t, "path")
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, configfile.EnvVar) {
		t.Errorf("the error should name %s, got:\n%s", configfile.EnvVar, stderr)
	}
}

// Every command in the tree prints its help and exits 0.
func TestConfigHelpExitsZero(t *testing.T) {
	for _, args := range [][]string{
		{"config", "--help"},
		{"config", "show", "--help"},
		{"config", "path", "--help"},
		{"config", "set", "--help"},
		{"config", "unset", "--help"},
		{"config", "edit", "--help"},
	} {
		var out bytes.Buffer
		if code := cli.Execute("dev", args, &out, io.Discard); code != 0 {
			t.Errorf("%v exit = %d, want 0", args, code)
		}
		if out.Len() == 0 {
			t.Errorf("%v produced no output", args)
		}
	}
}
