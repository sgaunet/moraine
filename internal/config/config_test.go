package config_test

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/config"
)

// defOpts returns Options pre-populated with the CLI defaults (the transport layer
// supplies these via flag defaults), so a test only tweaks the field under test.
func defOpts(src string) config.Options {
	return config.Options{
		Source:           src,
		Model:            config.DefaultModel,
		Gap:              config.DefaultGap,
		Sample:           config.DefaultSample,
		OllamaURL:        config.DefaultOllamaURL,
		Themes:           config.DefaultThemes,
		Fallback:         config.DefaultFallback,
		LogLevel:         config.DefaultLogLevel,
		ExifTool:         config.DefaultExifTool,
		Output:           config.DefaultOutput,
		MountainAltitude: config.DefaultMountainAltitude,
		PathTemplate:     config.DefaultPathTemplate,
	}
}

func TestNewDefaults(t *testing.T) {
	cfg, err := config.New(defOpts("/some/src"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Gap != 6*time.Hour {
		t.Errorf("Gap: want 6h, got %s", cfg.Gap)
	}
	if cfg.Sample != config.DefaultSample {
		t.Errorf("Sample: want %d, got %d", config.DefaultSample, cfg.Sample)
	}
	if cfg.Model != config.DefaultModel {
		t.Errorf("Model: want %q, got %q", config.DefaultModel, cfg.Model)
	}
	if cfg.FallbackTheme != "other" {
		t.Errorf("Fallback: want other, got %q", cfg.FallbackTheme)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel: want info, got %v", cfg.LogLevel)
	}
	if cfg.ExifToolPath != config.DefaultExifTool {
		t.Errorf("ExifToolPath: want %q, got %q", config.DefaultExifTool, cfg.ExifToolPath)
	}
	want := []string{"mountain", "special-events", "cook", "family"}
	if !reflect.DeepEqual(cfg.Themes, want) {
		t.Errorf("Themes: want %v, got %v", want, cfg.Themes)
	}
}

func TestNewMountainAltitude(t *testing.T) {
	cfg, err := config.New(defOpts("/some/src"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.MountainAltitude != config.DefaultMountainAltitude {
		t.Errorf("MountainAltitude: want %g, got %g", config.DefaultMountainAltitude, cfg.MountainAltitude)
	}
	if config.DefaultMountainAltitude != 1500 {
		t.Errorf("the documented heuristic threshold is 1500 m, got %g", config.DefaultMountainAltitude)
	}

	o := defOpts("/some/src")
	o.MountainAltitude = 900
	cfg, err = config.New(o)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.MountainAltitude != 900 {
		t.Errorf("custom MountainAltitude: want 900, got %g", cfg.MountainAltitude)
	}
}

func TestNewSidecarsPassthrough(t *testing.T) {
	for _, want := range []bool{true, false} {
		o := defOpts("/some/src")
		o.Sidecars = want
		cfg, err := config.New(o)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Sidecars != want {
			t.Errorf("Sidecars: want %v, got %v", want, cfg.Sidecars)
		}
	}
}

func TestNewIncrementalPassthrough(t *testing.T) {
	for _, want := range []bool{true, false} {
		o := defOpts("/some/src")
		o.Incremental = want
		cfg, err := config.New(o)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Incremental != want {
			t.Errorf("Incremental: want %v, got %v", want, cfg.Incremental)
		}
	}
}

func TestNewCustomThemes(t *testing.T) {
	o := defOpts("/src")
	o.Themes = "friends, hiking ,party"
	o.Fallback = "misc"
	cfg, err := config.New(o)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"friends", "hiking", "party"}
	if !reflect.DeepEqual(cfg.Themes, want) {
		t.Errorf("Themes: want %v, got %v", want, cfg.Themes)
	}
	if cfg.FallbackTheme != "misc" {
		t.Errorf("Fallback: want misc, got %q", cfg.FallbackTheme)
	}
}

func TestNewExifTool(t *testing.T) {
	// Custom path is honored.
	o := defOpts("/src")
	o.ExifTool = "/opt/bin/exiftool"
	cfg, err := config.New(o)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ExifToolPath != "/opt/bin/exiftool" {
		t.Errorf("ExifToolPath: want /opt/bin/exiftool, got %q", cfg.ExifToolPath)
	}
	// Empty value falls back to the default.
	o.ExifTool = "  "
	cfg, err = config.New(o)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ExifToolPath != config.DefaultExifTool {
		t.Errorf("empty exiftool: want default %q, got %q", config.DefaultExifTool, cfg.ExifToolPath)
	}
}

func TestNewErrors(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*config.Options)
	}{
		{"non-positive gap", func(o *config.Options) { o.Gap = 0 }},
		{"negative sample", func(o *config.Options) { o.Sample = -1 }},
		{"zero mountain altitude", func(o *config.Options) { o.MountainAltitude = 0 }},
		{"negative mountain altitude", func(o *config.Options) { o.MountainAltitude = -1 }},
		{"invalid theme slug", func(o *config.Options) { o.Themes = "Bad Slug" }},
		{"empty themes", func(o *config.Options) { o.Themes = " , " }},
		{"duplicate theme", func(o *config.Options) { o.Themes = "a,a" }},
		{"fallback collides", func(o *config.Options) { o.Themes = "a,other" }},
		{"invalid fallback slug", func(o *config.Options) { o.Fallback = "Nope!" }},
		{"invalid log level", func(o *config.Options) { o.LogLevel = "verbose" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := defOpts("/src")
			tc.mutate(&o)
			if _, err := config.New(o); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

func TestNewLogLevels(t *testing.T) {
	for in, want := range map[string]slog.Level{
		"debug": slog.LevelDebug,
		"info":  slog.LevelInfo,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
	} {
		o := defOpts("/src")
		o.LogLevel = in
		cfg, err := config.New(o)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if cfg.LogLevel != want {
			t.Errorf("%s: want %v, got %v", in, want, cfg.LogLevel)
		}
	}
}

func TestValidateDirectorySource(t *testing.T) {
	dir := t.TempDir()
	cfg, err := config.New(defOpts(dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !cfg.SourceIsDir {
		t.Error("want SourceIsDir true for a directory")
	}
	if cfg.DestRoot != filepath.Join(dir, config.DefaultDestName) {
		t.Errorf("DestRoot: want %q, got %q", filepath.Join(dir, config.DefaultDestName), cfg.DestRoot)
	}
}

func TestValidateFileSource(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "photo.jpg")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.New(defOpts(file))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if cfg.SourceIsDir {
		t.Error("want SourceIsDir false for a file")
	}
	if cfg.DestRoot != filepath.Join(dir, config.DefaultDestName) {
		t.Errorf("DestRoot: want %q, got %q", filepath.Join(dir, config.DefaultDestName), cfg.DestRoot)
	}
}

func TestValidateMissingSource(t *testing.T) {
	cfg, err := config.New(defOpts(filepath.Join(t.TempDir(), "nope")))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing source")
	}
}

func TestValidateExplicitDest(t *testing.T) {
	dir := t.TempDir()
	dest := t.TempDir()
	o := defOpts(dir)
	o.Dest = dest
	cfg, err := config.New(o)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.DestRoot != dest {
		t.Errorf("DestRoot: want %q, got %q", dest, cfg.DestRoot)
	}
}

// TestNewOutputFormat covers the stdout-format flag: the empty string means the
// default so a caller that never sets it still gets a usable Config, and anything
// unrecognised is a usage error rather than a silent fallback.
func TestNewOutputFormat(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		want    config.OutputFormat
		wantErr bool
	}{
		{"default when empty", "", config.OutputText, false},
		{"text", "text", config.OutputText, false},
		{"json", "json", config.OutputJSON, false},
		{"case insensitive", "JSON", config.OutputJSON, false},
		{"padded", "  json  ", config.OutputJSON, false},
		{"unknown rejected", "yaml", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := defOpts("/some/src")
			o.Output = tc.output
			cfg, err := config.New(o)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error for --output %q", tc.output)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.Output != tc.want {
				t.Errorf("Output = %q, want %q", cfg.Output, tc.want)
			}
		})
	}
}

// TestNewVerbosityFlags checks that --quiet/--verbose resolve to log levels. Their
// mutual exclusion is enforced by the transport (cobra), not here, so this only
// pins the mapping.
func TestNewVerbosityFlags(t *testing.T) {
	tests := []struct {
		name     string
		quiet    bool
		verbose  bool
		logLevel string
		want     slog.Level
	}{
		{"default", false, false, "info", slog.LevelInfo},
		{"explicit log level", false, false, "warn", slog.LevelWarn},
		{"quiet", true, false, "info", slog.LevelError},
		{"verbose", false, true, "info", slog.LevelDebug},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := defOpts("/some/src")
			o.Quiet, o.Verbose, o.LogLevel = tc.quiet, tc.verbose, tc.logLevel
			cfg, err := config.New(o)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.LogLevel != tc.want {
				t.Errorf("LogLevel = %v, want %v", cfg.LogLevel, tc.want)
			}
		})
	}
}

func TestNewJobsAndDryRun(t *testing.T) {
	t.Run("negative jobs rejected", func(t *testing.T) {
		o := defOpts("/some/src")
		o.Jobs = -1
		if _, err := config.New(o); err == nil {
			t.Fatal("expected an error for a negative --jobs")
		}
	})
	t.Run("zero jobs means auto", func(t *testing.T) {
		o := defOpts("/some/src")
		o.Jobs = 0
		cfg, err := config.New(o)
		if err != nil || cfg.Jobs != 0 {
			t.Fatalf("Jobs = %d, err = %v; want 0, nil", cfg.Jobs, err)
		}
	})
	t.Run("dry run carries through", func(t *testing.T) {
		o := defOpts("/some/src")
		o.DryRun = true
		cfg, err := config.New(o)
		if err != nil || !cfg.DryRun {
			t.Fatalf("DryRun = %v, err = %v; want true, nil", cfg.DryRun, err)
		}
	})
}

func TestNewMinConfidence(t *testing.T) {
	tests := []struct {
		name    string
		value   float64
		wantErr bool
	}{
		{"default off", 0, false},
		{"mid range", 0.7, false},
		{"upper bound", 1, false},
		{"negative rejected", -0.1, true},
		{"above one rejected", 1.1, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := defOpts("/some/src")
			o.MinConfidence = tc.value
			cfg, err := config.New(o)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("--min-confidence %g must be a usage error", tc.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.MinConfidence != tc.value {
				t.Errorf("MinConfidence = %g; want %g", cfg.MinConfidence, tc.value)
			}
		})
	}
}

func TestNewVoteIsOptIn(t *testing.T) {
	cfg, err := config.New(defOpts("/some/src"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Vote {
		t.Error("Vote must default to false: it costs one model call per sampled photo")
	}
	o := defOpts("/some/src")
	o.Vote = true
	cfg, err = config.New(o)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Vote {
		t.Error("Vote = false; want the flag to carry through")
	}
}

func TestNewPathTemplate(t *testing.T) {
	cfg, err := config.New(defOpts("/some/src"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The default must render the layout moraine has always used.
	if got, want := cfg.PathTemplate.String(), config.DefaultPathTemplate; got != want {
		t.Errorf("PathTemplate: want %q, got %q", want, got)
	}

	o := defOpts("/some/src")
	o.PathTemplate = "{year}/{month}"
	cfg, err = config.New(o)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cfg.PathTemplate.String(); got != "{year}/{month}" {
		t.Errorf("PathTemplate: got %q", got)
	}
}

// A bad template is a cross-field/syntax error, so it must come back from New (the
// transport turns that into exit 2) rather than surfacing per-cluster mid-run.
func TestNewRejectsBadPathTemplate(t *testing.T) {
	for _, tmpl := range []string{"{bogus}", "/{theme}", "{theme}//{year}", ".moraine/{theme}", "{theme}/../x"} {
		o := defOpts("/some/src")
		o.PathTemplate = tmpl
		if _, err := config.New(o); err == nil {
			t.Errorf("New with --path-template %q: want an error, got nil", tmpl)
		}
	}
}

// TestNewDecider covers the describe-then-decide settings. Every rejected case is a
// usage error that names the flag, and none may echo an API key.
func TestNewDecider(t *testing.T) {
	manyThemes := func(n int) string {
		s := make([]string, 0, n)
		for i := range n {
			s = append(s, fmt.Sprintf("t%d", i))
		}
		return strings.Join(s, ",")
	}
	tests := []struct {
		name    string
		edit    func(*config.Options)
		wantErr string // substring; "" means the options are valid
		check   func(*testing.T, config.Config)
	}{
		{name: "default is off", edit: func(*config.Options) {}, check: func(t *testing.T, c config.Config) {
			t.Helper()
			if c.Decider != config.DeciderOff {
				t.Errorf("Decider = %q; want off", c.Decider)
			}
		}},
		{name: "unknown decider", edit: func(o *config.Options) { o.Decider = "bogus" }, wantErr: "--decider"},
		{name: "laya defaults", edit: func(o *config.Options) { o.Decider = "laya" },
			check: func(t *testing.T, c config.Config) {
				t.Helper()
				if c.DeciderURL != config.DefaultLayaURL || c.DeciderModel != config.DefaultLayaModel {
					t.Errorf("laya defaults = (%q, %q)", c.DeciderURL, c.DeciderModel)
				}
			}},
		{name: "jev defaults", edit: func(o *config.Options) { o.Decider = "jev"; o.DeciderAPIKey = "jev-k3y" },
			check: func(t *testing.T, c config.Config) {
				t.Helper()
				if c.DeciderURL != config.DefaultJevURL || c.DeciderModel != config.DefaultJevModel {
					t.Errorf("jev defaults = (%q, %q)", c.DeciderURL, c.DeciderModel)
				}
			}},
		{name: "explicit url and model are kept", edit: func(o *config.Options) {
			o.Decider, o.DeciderURL, o.DeciderModel = "laya", "http://192.168.0.47:8000", "english"
		}, check: func(t *testing.T, c config.Config) {
			t.Helper()
			if c.DeciderURL != "http://192.168.0.47:8000" || c.DeciderModel != "english" {
				t.Errorf("got (%q, %q)", c.DeciderURL, c.DeciderModel)
			}
		}},
		{name: "relative url", edit: func(o *config.Options) { o.Decider, o.DeciderURL = "laya", "127.0.0.1:8000" },
			wantErr: "--decider-url"},
		{name: "ftp url", edit: func(o *config.Options) { o.Decider, o.DeciderURL = "laya", "ftp://host" },
			wantErr: "--decider-url"},
		{name: "url without host", edit: func(o *config.Options) { o.Decider, o.DeciderURL = "laya", "http://" },
			wantErr: "--decider-url"},
		{name: "confidence above 1", edit: func(o *config.Options) { o.DeciderMinConfidence = 1.5 },
			wantErr: "--decider-min-confidence"},
		{name: "confidence below 0", edit: func(o *config.Options) { o.DeciderMinConfidence = -0.1 },
			wantErr: "--decider-min-confidence"},
		{name: "jev without key", edit: func(o *config.Options) { o.Decider = "jev" }, wantErr: "TYPESAFE_API_KEY"},
		{name: "laya without key is fine", edit: func(o *config.Options) { o.Decider = "laya" }},
		{name: "laya with 99 themes fits", edit: func(o *config.Options) {
			o.Decider, o.Themes = "laya", manyThemes(99)
		}},
		{name: "laya with 100 themes plus fallback", edit: func(o *config.Options) {
			o.Decider, o.Themes = "laya", manyThemes(100)
		}, wantErr: "--themes"},
		{name: "jev with 254 themes fits", edit: func(o *config.Options) {
			o.Decider, o.DeciderAPIKey, o.Themes = "jev", "jev-k3y", manyThemes(254)
		}},
		{name: "jev with 255 themes plus fallback", edit: func(o *config.Options) {
			o.Decider, o.DeciderAPIKey, o.Themes = "jev", "jev-k3y", manyThemes(255)
		}, wantErr: "--themes"},
		{name: "off ignores the other settings", edit: func(o *config.Options) {
			o.Decider, o.DeciderURL, o.Themes = "off", "not a url", manyThemes(300)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := defOpts("/some/src")
			tc.edit(&o)
			cfg, err := config.New(o)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tc.check != nil {
					tc.check(t, cfg)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v; want it to mention %q", err, tc.wantErr)
			}
			if o.DeciderAPIKey != "" && strings.Contains(err.Error(), o.DeciderAPIKey) {
				t.Errorf("error leaks the API key: %v", err)
			}
		})
	}
}

// TestNewDeciderCarriesTheKey: the key reaches the Config (so app can hand it to the
// client) but only from Options, never from a flag.
func TestNewDeciderCarriesTheKey(t *testing.T) {
	o := defOpts("/some/src")
	o.Decider, o.DeciderAPIKey, o.DeciderMinConfidence = "laya", "k3y-xyz", 0.6
	cfg, err := config.New(o)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DeciderAPIKey != "k3y-xyz" || cfg.DeciderMinConfidence != 0.6 {
		t.Errorf("got key %q, min confidence %g", cfg.DeciderAPIKey, cfg.DeciderMinConfidence)
	}
}

func TestNewThemeDescriptions(t *testing.T) {
	tests := []struct {
		name    string
		entries []string
		want    map[string]string
		wantErr string
	}{
		{"none", nil, nil, ""},
		{"split on the first equals sign, commas kept", []string{"cook=food, meals = dishes"},
			map[string]string{"cook": "food, meals = dishes"}, ""},
		{"trimmed", []string{" cook =  food "}, map[string]string{"cook": "food"}, ""},
		{"the fallback may be described", []string{"other=screenshots, receipts"},
			map[string]string{"other": "screenshots, receipts"}, ""},
		{"several", []string{"cook=food", "family=people"}, map[string]string{"cook": "food", "family": "people"}, ""},
		{"no equals sign", []string{"cook"}, nil, "--theme-description"},
		{"unconfigured theme", []string{"nope=x"}, nil, "nope"},
		{"empty text", []string{"cook= "}, nil, "--theme-description"},
		{"newline in text", []string{"cook=food\nand more"}, nil, "--theme-description"},
		{"120 characters fit", []string{"cook=" + strings.Repeat("é", 120)},
			map[string]string{"cook": strings.Repeat("é", 120)}, ""},
		{"121 characters do not", []string{"cook=" + strings.Repeat("é", 121)}, nil, "120"},
		{"a slug given twice", []string{"cook=food", "cook=meals"}, nil, "cook"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := defOpts("/some/src")
			o.ThemeDescriptions = tc.entries
			cfg, err := config.New(o)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v; want it to mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(cfg.ThemeDescriptions, tc.want) {
				t.Errorf("ThemeDescriptions = %v; want %v", cfg.ThemeDescriptions, tc.want)
			}
		})
	}
}
