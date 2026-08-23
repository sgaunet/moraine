package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sgaunet/moraine/internal/config"
)

func TestNewUndoDefaults(t *testing.T) {
	cfg, err := config.NewUndo(config.UndoOptions{Dest: "out", LogLevel: config.DefaultLogLevel})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Delete {
		t.Error("Delete must default to false (dry-run)")
	}
	if !filepath.IsAbs(cfg.DestRoot) || filepath.Base(cfg.DestRoot) != "out" {
		t.Errorf("DestRoot = %q, want an absolute path ending in out", cfg.DestRoot)
	}
	if cfg.Output != config.OutputText {
		t.Errorf("Output = %q, want the text default", cfg.Output)
	}
}

func TestNewUndoErrors(t *testing.T) {
	tests := map[string]config.UndoOptions{
		"invalid log level":  {Dest: "out", LogLevel: "loud"},
		"invalid output":     {Dest: "out", LogLevel: config.DefaultLogLevel, Output: "yaml"},
		"empty destination":  {Dest: "  ", LogLevel: config.DefaultLogLevel},
		"quiet plus a level": {Dest: "out", LogLevel: "bogus", Quiet: true},
	}
	for name, opts := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := config.NewUndo(opts); err == nil && name != "quiet plus a level" {
				t.Error("expected an error")
			}
		})
	}
}

func TestNewUndoVerbosityShorthands(t *testing.T) {
	quiet, err := config.NewUndo(config.UndoOptions{Dest: "out", Quiet: true})
	if err != nil {
		t.Fatal(err)
	}
	verbose, err := config.NewUndo(config.UndoOptions{Dest: "out", Verbose: true})
	if err != nil {
		t.Fatal(err)
	}
	if quiet.LogLevel <= verbose.LogLevel {
		t.Errorf("--quiet (%v) must be less chatty than --verbose (%v)", quiet.LogLevel, verbose.LogLevel)
	}
}

func TestUndoValidate(t *testing.T) {
	t.Run("an existing directory passes", func(t *testing.T) {
		cfg := config.UndoConfig{DestRoot: t.TempDir()}
		if err := cfg.Validate(); err != nil {
			t.Errorf("validate: %v", err)
		}
	})

	t.Run("missing destination is an error", func(t *testing.T) {
		cfg := config.UndoConfig{DestRoot: filepath.Join(t.TempDir(), "nope")}
		if err := cfg.Validate(); err == nil {
			t.Error("expected an error for a missing destination")
		}
	})

	t.Run("file destination is rejected", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "a.txt")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg := config.UndoConfig{DestRoot: file}
		if err := cfg.Validate(); err == nil {
			t.Error("expected an error for a non-directory destination")
		}
	})
}
