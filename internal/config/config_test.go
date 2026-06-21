package config_test

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/config"
)

func TestParseDefaults(t *testing.T) {
	cfg, err := config.Parse([]string{"/some/src"})
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

func TestParseCustomThemes(t *testing.T) {
	cfg, err := config.Parse([]string{"-themes", "friends, hiking ,party", "-fallback-theme", "misc", "/src"})
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

func TestParseExifTool(t *testing.T) {
	// Custom path is honored.
	cfg, err := config.Parse([]string{"-exiftool", "/opt/bin/exiftool", "/src"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ExifToolPath != "/opt/bin/exiftool" {
		t.Errorf("ExifToolPath: want /opt/bin/exiftool, got %q", cfg.ExifToolPath)
	}
	// Empty value falls back to the default.
	cfg, err = config.Parse([]string{"-exiftool", "  ", "/src"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ExifToolPath != config.DefaultExifTool {
		t.Errorf("empty -exiftool: want default %q, got %q", config.DefaultExifTool, cfg.ExifToolPath)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"missing source", []string{}},
		{"two sources", []string{"a", "b"}},
		{"unknown flag addr", []string{"-addr", ":8080", "/src"}},
		{"unknown flag home", []string{"-home", "1,2", "/src"}},
		{"non-positive gap", []string{"-gap", "0", "/src"}},
		{"negative sample", []string{"-sample", "-1", "/src"}},
		{"invalid theme slug", []string{"-themes", "Bad Slug", "/src"}},
		{"empty themes", []string{"-themes", " , ", "/src"}},
		{"duplicate theme", []string{"-themes", "a,a", "/src"}},
		{"fallback collides", []string{"-themes", "a,other", "/src"}},
		{"invalid fallback slug", []string{"-fallback-theme", "Nope!", "/src"}},
		{"invalid log level", []string{"-log-level", "verbose", "/src"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := config.Parse(tc.args); err == nil {
				t.Fatalf("expected error for %v", tc.args)
			}
		})
	}
}

func TestParseHelp(t *testing.T) {
	for _, arg := range []string{"-help", "-h"} {
		if _, err := config.Parse([]string{arg}); !errors.Is(err, config.ErrHelp) {
			t.Errorf("Parse(%q): want ErrHelp, got %v", arg, err)
		}
	}
}

func TestParseVersion(t *testing.T) {
	cfg, err := config.Parse([]string{"-version"}) // no source required
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.ShowVersion {
		t.Error("want ShowVersion true for -version")
	}
}

func TestWriteUsage(t *testing.T) {
	var buf bytes.Buffer
	config.WriteUsage(&buf)
	out := buf.String()

	wants := []string{
		"-dest", "-gap", "-sample", "-model", "-ollama-url",
		"-themes", "-fallback-theme", "-log-level", "-version", "-exiftool",
		config.DefaultThemes,              // default theme set
		"<theme>/<year>/<year-month-day>", // destination layout
		"RAW",                             // RAW support documented
		"Exit codes", "Examples",          // sections
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("WriteUsage output missing %q", w)
		}
	}
}

func TestParseLogLevels(t *testing.T) {
	for in, want := range map[string]slog.Level{
		"debug": slog.LevelDebug,
		"info":  slog.LevelInfo,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
	} {
		cfg, err := config.Parse([]string{"-log-level", in, "/src"})
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
	cfg, err := config.Parse([]string{dir})
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
	cfg, err := config.Parse([]string{file})
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
	cfg, err := config.Parse([]string{filepath.Join(t.TempDir(), "nope")})
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
	cfg, err := config.Parse([]string{"-dest", dest, dir})
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
