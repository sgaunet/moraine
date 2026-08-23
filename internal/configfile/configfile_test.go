package configfile_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/configfile"
)

// write puts contents in a file inside a fresh temp dir and returns its path.
func write(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "moraine.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// isolate removes every implicit source, so a test observes only what it sets up and
// never the machine's real configuration file. MORAINE_CONFIG is *unset* rather than
// blanked: an empty value means "no configuration file at all", which would also
// short-circuit the implicit locations some of these tests are about.
func isolate(t *testing.T) {
	t.Helper()
	unsetEnv(t, configfile.EnvVar)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", t.TempDir())
}

// unsetEnv removes key for the duration of the test. t.Setenv can only set a value,
// never remove one — but it does register the restoration of whatever was there
// before, so setting it first and then removing it gets both halves right.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
}

// No configuration file is the ordinary case, and must be silent success rather than
// an error — otherwise moraine would refuse to run without one.
func TestLoadWithNoFileIsNotAnError(t *testing.T) {
	isolate(t)
	f, path, err := configfile.Load("")
	if err != nil {
		t.Fatalf("Load = %v, want no error", err)
	}
	if f != nil || path != "" {
		t.Errorf("Load = (%v, %q), want (nil, \"\")", f, path)
	}
	// The accessors must tolerate the nil, so callers need no nil check.
	if s := f.SortSection(); s.Gap != nil || s.Themes != nil {
		t.Errorf("a nil File must yield an empty section, got %+v", s)
	}
}

// A file named on purpose must exist: silently ignoring --config would hide a typo in
// the path and leave the user wondering why their settings did nothing.
func TestLoadExplicitMissingFileIsAnError(t *testing.T) {
	isolate(t)
	if _, _, err := configfile.Load(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Fatal("Load with a missing explicit path = nil error, want a failure")
	}
}

// An implicit location is optional by nature.
func TestLoadImplicitMissingFileIsNotAnError(t *testing.T) {
	isolate(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // exists, but holds no moraine.yaml
	f, path, err := configfile.Load("")
	if err != nil || f != nil || path != "" {
		t.Fatalf("Load = (%v, %q, %v), want (nil, \"\", nil)", f, path, err)
	}
}

func TestLoadReadsSharedAndSectionKeys(t *testing.T) {
	isolate(t)
	path := write(t, `
log_level: warn
dest: /library
sort:
  gap: 2h30m
  sample: 5
  themes: [hiking, party]
  path_template: "{year}/{month}"
  sidecars: false
  mountain_altitude: 900
  min_confidence: 0.7
  vote: true
clean:
  log_level: debug
`)
	f, from, err := configfile.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if from != path {
		t.Errorf("path = %q, want %q", from, path)
	}

	s := f.SortSection()
	if s.Gap == nil || s.Gap.Duration != 2*time.Hour+30*time.Minute {
		t.Errorf("gap = %v, want 2h30m", s.Gap)
	}
	if s.Sample == nil || *s.Sample != 5 {
		t.Errorf("sample = %v, want 5", s.Sample)
	}
	if strings.Join(s.Themes, ",") != "hiking,party" {
		t.Errorf("themes = %v", s.Themes)
	}
	if s.PathTemplate == nil || *s.PathTemplate != "{year}/{month}" {
		t.Errorf("path_template = %v", s.PathTemplate)
	}
	if s.Sidecars == nil || *s.Sidecars {
		t.Errorf("sidecars = %v, want false", s.Sidecars)
	}
	if s.MountainAltitude == nil || *s.MountainAltitude != 900 {
		t.Errorf("mountain_altitude = %v, want 900", s.MountainAltitude)
	}
	if s.MinConfidence == nil || *s.MinConfidence != 0.7 {
		t.Errorf("min_confidence = %v, want 0.7", s.MinConfidence)
	}
	if s.Vote == nil || !*s.Vote {
		t.Errorf("vote = %v, want true", s.Vote)
	}
	// A shared key reaches the section that does not override it...
	if s.LogLevel == nil || *s.LogLevel != "warn" {
		t.Errorf("sort log_level = %v, want the shared \"warn\"", s.LogLevel)
	}
	if s.Dest == nil || *s.Dest != "/library" {
		t.Errorf("sort dest = %v, want the shared \"/library\"", s.Dest)
	}
	// ... and a section overrides it.
	if c := f.CleanSection(); c.LogLevel == nil || *c.LogLevel != "debug" {
		t.Errorf("clean log_level = %v, want its own \"debug\"", c.LogLevel)
	}
}

// undo takes its destination as an argument, so it has no dest to configure: the
// shared key must not leak into it.
func TestUndoSectionHasNoDest(t *testing.T) {
	isolate(t)
	path := write(t, "dest: /library\noutput: json\n")
	f, _, err := configfile.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	u := f.UndoSection()
	if u.Output == nil || *u.Output != "json" {
		t.Errorf("undo output = %v, want the shared \"json\"", u.Output)
	}
	// Undo has no Dest field at all; assert the shared one did not become an output
	// setting by accident.
	if u.LogLevel != nil {
		t.Errorf("undo log_level = %v, want nil", u.LogLevel)
	}
}

// Absent means absent: a key the file omits must stay nil so a flag default wins.
func TestOmittedKeysStayNil(t *testing.T) {
	isolate(t)
	path := write(t, "sort:\n  sample: 1\n")
	f, _, err := configfile.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s := f.SortSection()
	if s.Gap != nil || s.Model != nil || s.Sidecars != nil || s.Themes != nil {
		t.Errorf("omitted keys must be nil, got %+v", s)
	}
}

func TestLoadRejects(t *testing.T) {
	tests := []struct {
		name     string
		contents string
	}{
		{"unknown top-level key", "gpa: 6h\n"},
		{"unknown key in a section", "sort:\n  gapp: 6h\n"},
		{"a mode flag is not configurable", "sort:\n  dry_run: true\n"},
		{"delete is not configurable", "clean:\n  delete: true\n"},
		{"incremental is not configurable", "sort:\n  incremental: true\n"},
		{"move is not configurable", "sort:\n  move: true\n"},
		{"quiet is not configurable", "sort:\n  quiet: true\n"},
		{"verbose is not configurable", "sort:\n  verbose: true\n"},
		{"malformed yaml", "sort:\n\tgap: 6h\n"},
		{"gap is not a duration", "sort:\n  gap: nonsense\n"},
		{"gap as a bare number", "sort:\n  gap: 6\n"},
		{"sample is not a number", "sort:\n  sample: many\n"},
		{"themes is not a list", "sort:\n  themes: 3\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isolate(t)
			if _, _, err := configfile.Load(write(t, tc.contents)); err == nil {
				t.Errorf("Load(%q) = nil error, want a rejection", tc.contents)
			}
		})
	}
}

// An empty file is a valid file that sets nothing — a user commenting everything out
// must not get an error.
func TestEmptyFileIsValid(t *testing.T) {
	isolate(t)
	for _, contents := range []string{"", "\n", "# only a comment\n"} {
		f, _, err := configfile.Load(write(t, contents))
		if err != nil {
			t.Fatalf("Load(%q) = %v, want no error", contents, err)
		}
		if s := f.SortSection(); s.Gap != nil {
			t.Errorf("an empty file must set nothing, got %+v", s)
		}
	}
}

// MORAINE_CONFIG is both a user's override and the way a test suite opts out. Set to
// a path it must be honoured; set to empty it disables the file entirely.
func TestEnvVarSelectsAndDisables(t *testing.T) {
	isolate(t)
	path := write(t, "sort:\n  sample: 9\n")

	t.Setenv(configfile.EnvVar, path)
	f, from, err := configfile.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if from != path || f.SortSection().Sample == nil || *f.SortSection().Sample != 9 {
		t.Errorf("the env var was not honoured: from=%q file=%+v", from, f)
	}

	// A file named through the env var must exist, for the same reason --config must.
	t.Setenv(configfile.EnvVar, filepath.Join(t.TempDir(), "absent.yaml"))
	if _, _, err := configfile.Load(""); err == nil {
		t.Error("a missing MORAINE_CONFIG file = nil error, want a failure")
	}

	t.Setenv(configfile.EnvVar, "")
	if f, from, err := configfile.Load(""); err != nil || f != nil || from != "" {
		t.Errorf("an empty MORAINE_CONFIG must disable the file, got (%v, %q, %v)", f, from, err)
	}
}

// --config beats the environment, which beats the implicit locations.
func TestExplicitPathWinsOverTheEnvironment(t *testing.T) {
	isolate(t)
	t.Setenv(configfile.EnvVar, write(t, "sort:\n  sample: 1\n"))
	explicit := write(t, "sort:\n  sample: 2\n")

	f, from, err := configfile.Load(explicit)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if from != explicit || *f.SortSection().Sample != 2 {
		t.Errorf("--config must win: from=%q sample=%v", from, f.SortSection().Sample)
	}
}

// XDG_CONFIG_HOME is honoured, and ~/.config is the fallback. os.UserConfigDir is
// deliberately not used: on macOS it points at ~/Library/Application Support.
func TestImplicitLocations(t *testing.T) {
	t.Run("XDG_CONFIG_HOME", func(t *testing.T) {
		isolate(t)
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "moraine.yaml"), []byte("sort:\n  sample: 4\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("XDG_CONFIG_HOME", dir)
		f, from, err := configfile.Load("")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if from != filepath.Join(dir, "moraine.yaml") || *f.SortSection().Sample != 4 {
			t.Errorf("XDG location not read: from=%q", from)
		}
	})

	t.Run("~/.config", func(t *testing.T) {
		isolate(t)
		home := t.TempDir()
		if err := os.MkdirAll(filepath.Join(home, ".config"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, ".config", "moraine.yaml"),
			[]byte("sort:\n  sample: 7\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HOME", home)
		f, from, err := configfile.Load("")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if from != filepath.Join(home, ".config", "moraine.yaml") || *f.SortSection().Sample != 7 {
			t.Errorf("~/.config location not read: from=%q", from)
		}
	})
}
