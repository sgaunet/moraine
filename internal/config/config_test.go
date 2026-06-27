package config_test

import (
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/config"
)

// defOpts returns Options pre-populated with the CLI defaults (the transport layer
// supplies these via flag defaults), so a test only tweaks the field under test.
func defOpts(src string) config.Options {
	return config.Options{
		Source:    src,
		Model:     config.DefaultModel,
		Gap:       config.DefaultGap,
		Sample:    config.DefaultSample,
		OllamaURL: config.DefaultOllamaURL,
		Themes:    config.DefaultThemes,
		Fallback:  config.DefaultFallback,
		LogLevel:  config.DefaultLogLevel,
		ExifTool:  config.DefaultExifTool,
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
