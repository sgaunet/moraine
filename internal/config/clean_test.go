package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sgaunet/moraine/internal/config"
)

func TestNewCleanDefaults(t *testing.T) {
	cfg, err := config.NewClean(config.CleanOptions{Source: "some/src", LogLevel: config.DefaultLogLevel})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Delete {
		t.Error("Delete must default to false (dry-run)")
	}
	if cfg.DestRoot != "" {
		t.Errorf("DestRoot should be empty until Validate resolves it, got %q", cfg.DestRoot)
	}
	if !filepath.IsAbs(cfg.Source) {
		t.Errorf("Source should be made absolute, got %q", cfg.Source)
	}
}

func TestNewCleanDeleteAndDest(t *testing.T) {
	cfg, err := config.NewClean(config.CleanOptions{
		Source: "src", Dest: "out", Delete: true, LogLevel: config.DefaultLogLevel,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Delete {
		t.Error("--delete should set Delete true")
	}
	if !filepath.IsAbs(cfg.DestRoot) || filepath.Base(cfg.DestRoot) != "out" {
		t.Errorf("DestRoot = %q, want an absolute path ending in out", cfg.DestRoot)
	}
}

func TestNewCleanErrors(t *testing.T) {
	if _, err := config.NewClean(config.CleanOptions{Source: "a", LogLevel: "loud"}); err == nil {
		t.Error("expected an error for an invalid log level")
	}
}

func TestCleanValidate(t *testing.T) {
	t.Run("default dest resolves to <source>/_sorted", func(t *testing.T) {
		src := t.TempDir()
		dst := filepath.Join(src, config.DefaultDestName)
		if err := os.MkdirAll(dst, 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := config.CleanConfig{Source: src}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("validate: %v", err)
		}
		if cfg.DestRoot != dst {
			t.Errorf("DestRoot = %q, want %q", cfg.DestRoot, dst)
		}
	})

	t.Run("missing source is an error", func(t *testing.T) {
		cfg := config.CleanConfig{Source: filepath.Join(t.TempDir(), "nope")}
		if err := cfg.Validate(); err == nil {
			t.Error("expected an error for a missing source")
		}
	})

	t.Run("file source is rejected", func(t *testing.T) {
		src := t.TempDir()
		file := filepath.Join(src, "a.txt")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg := config.CleanConfig{Source: file}
		if err := cfg.Validate(); err == nil {
			t.Error("expected an error for a non-directory source")
		}
	})

	t.Run("missing destination is an actionable error", func(t *testing.T) {
		src := t.TempDir()
		cfg := config.CleanConfig{Source: src, DestRoot: filepath.Join(src, "does-not-exist")}
		if err := cfg.Validate(); err == nil {
			t.Error("expected an error for a missing destination")
		}
	})
}
