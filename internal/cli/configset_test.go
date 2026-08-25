package cli_test

import (
	"os"
	"strings"
	"testing"
)

// read returns the configuration file's current contents.
func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The ordinary path: write settings, and see them reported as coming from the file.
func TestConfigSetWritesWhatWasTyped(t *testing.T) {
	path := configAt(t, "")

	if _, _, code := runConfig(t, "set", "sort", "--gap", "8h", "--jobs", "4", "--vote"); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	shown, _, code := runConfig(t, "show", "sort", "--output=json")
	if code != 0 {
		t.Fatalf("show exit = %d", code)
	}
	got := settings(t, shown)
	for key, want := range map[string]string{"sort.gap": "8h", "sort.jobs": "4", "sort.vote": "true"} {
		if got[key].Value != want || got[key].Origin != "file" {
			t.Errorf("%s = %+v, want %q from the file", key, got[key], want)
		}
	}
	// A setting nobody typed must not have been written along with them.
	if got["sort.sample"].Origin != "default" {
		t.Errorf("sort.sample was written without being asked for: %+v", got["sort.sample"])
	}

	// The duration is recorded the way it was typed, not as 8h0m0s.
	if body := read(t, path); !strings.Contains(body, "gap: 8h\n") {
		t.Errorf("the file records the gap awkwardly:\n%s", body)
	}
}

// A top-level setting applies to every command, and is written unprefixed.
func TestConfigSetSharedWritesAtTheTopLevel(t *testing.T) {
	path := configAt(t, "")
	if _, _, code := runConfig(t, "set", "shared", "--log-level", "warn"); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if body := read(t, path); !strings.Contains(body, "\nlog_level: warn\n") {
		t.Errorf("want a top-level log_level, got:\n%s", body)
	}
}

// The reason this feature edits the YAML tree instead of re-marshalling it: the file
// is hand-maintained, and the comments in it are the part moraine did not write.
func TestConfigSetKeepsCommentsAndUntouchedKeys(t *testing.T) {
	path := configAt(t, `# my library
log_level: warn # keep it quiet
sort:
  # a day out is one event
  gap: 6h
  themes: [mountain, cook]
`)
	if _, _, code := runConfig(t, "set", "sort", "--gap", "12h"); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	body := read(t, path)
	for _, want := range []string{
		"# my library",
		"# keep it quiet",
		"# a day out is one event",
		"themes: [mountain, cook]",
		"gap: 12h",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("%q did not survive the write:\n%s", want, body)
		}
	}
	if strings.Contains(body, "gap: 6h") {
		t.Errorf("the old value is still there:\n%s", body)
	}
}

// A file moraine writes must be one moraine can read. A value no run would accept is
// refused before anything is saved, rather than breaking the next run.
func TestConfigSetRefusesAValueNoRunCouldUse(t *testing.T) {
	path := configAt(t, "sort:\n  gap: 6h\n")
	before := read(t, path)

	for name, args := range map[string][]string{
		"out of range":               {"set", "sort", "--min-confidence", "2"},
		"collides with the fallback": {"set", "sort", "--fallback-theme", "mountain"},
		"not a template":             {"set", "sort", "--path-template", "/absolute"},
	} {
		t.Run(name, func(t *testing.T) {
			_, stderr, code := runConfig(t, args...)
			if code != 2 {
				t.Fatalf("exit = %d, want 2", code)
			}
			if !strings.Contains(stderr, "nothing was written") {
				t.Errorf("the refusal should say the file was left alone, got:\n%s", stderr)
			}
			if read(t, path) != before {
				t.Errorf("the file was changed by a refused write:\n%s", read(t, path))
			}
		})
	}
}

// A malformed value is caught by the flag itself, in the words sort would use.
func TestConfigSetRefusesAMalformedValue(t *testing.T) {
	configAt(t, "")
	_, stderr, code := runConfig(t, "set", "sort", "--gap", "nope")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "--gap") || !strings.Contains(stderr, "nope") {
		t.Errorf("want pflag's own complaint, got:\n%s", stderr)
	}
}

// `config set sort` with nothing to set is a mistake worth naming, not a no-op.
func TestConfigSetNeedsASetting(t *testing.T) {
	configAt(t, "")
	_, stderr, code := runConfig(t, "set", "sort")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "no setting given") {
		t.Errorf("stderr = %s", stderr)
	}
}

// --dry-run reports the result and writes nothing, which is how the write can be
// inspected before it happens.
func TestConfigSetDryRunWritesNothing(t *testing.T) {
	path := configAt(t, "")
	stdout, _, code := runConfig(t, "set", "sort", "--gap", "8h", "--dry-run")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stdout, "sort.gap=8h origin=file") {
		t.Errorf("a dry run should still report the result it would produce:\n%s", stdout)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a dry run created the configuration file")
	}
}

// Writing what the file already says is not a write: it must not touch the file, so
// a script that re-applies its settings does not churn them.
func TestConfigSetReportsNoWriteWhenNothingWouldChange(t *testing.T) {
	path := configAt(t, "sort:\n  gap: 8h\n")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, code := runConfig(t, "set", "sort", "--gap", "8h"); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("a set that changes nothing rewrote the file")
	}
}

// --output on `config set` names the setting, not the rendering. Both readings are
// plausible, so using it must say which one happened.
func TestConfigSetSaysThatOutputNamedTheSetting(t *testing.T) {
	path := configAt(t, "")
	stdout, stderr, code := runConfig(t, "set", "shared", "--output", "json")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if strings.HasPrefix(strings.TrimSpace(stdout), "{") {
		t.Errorf("--output changed this command's own rendering:\n%s", stdout)
	}
	if !strings.Contains(stderr, "wrote the output setting") {
		t.Errorf("the ambiguity was not explained:\n%s", stderr)
	}
	if body := read(t, path); !strings.Contains(body, "output: json") {
		t.Errorf("the setting was not written:\n%s", body)
	}
}

// A setting pinned by mistake has to have a way back that is not a text editor.
func TestConfigUnsetReturnsASettingToItsDefault(t *testing.T) {
	path := configAt(t, "sort:\n  gap: 8h\n  jobs: 4\n")

	if _, _, code := runConfig(t, "unset", "sort", "gap"); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	shown, _, code := runConfig(t, "show", "sort", "--output=json")
	if code != 0 {
		t.Fatalf("show exit = %d", code)
	}
	got := settings(t, shown)
	if got["sort.gap"].Value != "6h" || got["sort.gap"].Origin != "default" {
		t.Errorf("sort.gap = %+v, want it back at the default", got["sort.gap"])
	}
	if got["sort.jobs"].Value != "4" {
		t.Errorf("unsetting one setting disturbed another: %+v", got["sort.jobs"])
	}
	if body := read(t, path); strings.Contains(body, "gap") {
		t.Errorf("the key is still in the file:\n%s", body)
	}
}

// Removing the last setting of a section takes the section with it, rather than
// leaving "sort: {}" for the user to tidy.
func TestConfigUnsetPrunesAnEmptiedSection(t *testing.T) {
	path := configAt(t, "output: json\nsort:\n  gap: 8h\n")
	if _, _, code := runConfig(t, "unset", "sort", "gap"); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if body := read(t, path); strings.Contains(body, "sort") {
		t.Errorf("the emptied section is still there:\n%s", body)
	}
}

// A setting may be named either way round, since the flag and the key are the same
// word spelled two ways.
func TestConfigUnsetAcceptsEitherSpelling(t *testing.T) {
	for _, name := range []string{"path-template", "path_template"} {
		t.Run(name, func(t *testing.T) {
			path := configAt(t, "sort:\n  path_template: \"{year}\"\n")
			if _, _, code := runConfig(t, "unset", "sort", name); code != 0 {
				t.Fatalf("exit = %d", code)
			}
			if body := read(t, path); strings.Contains(body, "path_template") {
				t.Errorf("still set:\n%s", body)
			}
		})
	}
}

// An unknown section or setting is a usage error that names what was expected.
func TestConfigUnsetRefusesWhatItDoesNotKnow(t *testing.T) {
	configAt(t, "")
	for name, args := range map[string][]string{
		"section": {"unset", "nonsense", "gap"},
		"setting": {"unset", "sort", "nonsense"},
	} {
		t.Run(name, func(t *testing.T) {
			_, stderr, code := runConfig(t, args...)
			if code != 2 {
				t.Fatalf("exit = %d, want 2", code)
			}
			if !strings.Contains(stderr, "nonsense") {
				t.Errorf("stderr = %s", stderr)
			}
		})
	}
}

// Unsetting what was never set is nothing to do, not a failure.
func TestConfigUnsetToleratesASettingThatWasNotThere(t *testing.T) {
	configAt(t, "sort:\n  gap: 8h\n")
	if _, _, code := runConfig(t, "unset", "sort", "jobs"); code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
}
