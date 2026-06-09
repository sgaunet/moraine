package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/config"
)

func TestParseDefaults(t *testing.T) {
	cfg, err := config.Parse([]string{"/some/source"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Addr != "127.0.0.1:8080" {
		t.Errorf("Addr = %q; want loopback default", cfg.Addr)
	}
	if cfg.Gap != 4*time.Hour {
		t.Errorf("Gap = %v; want 4h", cfg.Gap)
	}
	if cfg.Model != "qwen2.5vl:7b" {
		t.Errorf("Model = %q; want qwen2.5vl:7b", cfg.Model)
	}
	if cfg.Sample != 3 {
		t.Errorf("Sample = %d; want 3", cfg.Sample)
	}
	if cfg.Home != nil {
		t.Errorf("Home = %v; want nil by default", cfg.Home)
	}
}

func TestParseDefaultDestUnderSource(t *testing.T) {
	cfg, err := config.Parse([]string{"/photos/2025"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want, _ := filepath.Abs(filepath.Join("/photos/2025", "_trie"))
	if cfg.DestRoot != want {
		t.Errorf("DestRoot = %q; want %q (<source>/_trie)", cfg.DestRoot, want)
	}
}

func TestParseExplicitDest(t *testing.T) {
	cfg, err := config.Parse([]string{"-dest", "/elsewhere/sorted", "/photos/2025"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want, _ := filepath.Abs("/elsewhere/sorted")
	if cfg.DestRoot != want {
		t.Errorf("DestRoot = %q; want %q", cfg.DestRoot, want)
	}
}

func TestParseMissingSource(t *testing.T) {
	_, err := config.Parse([]string{})
	if err == nil {
		t.Fatal("expected error for missing positional source argument")
	}
}

func TestParseGapNonPositive(t *testing.T) {
	for _, gap := range []string{"0", "-1h"} {
		if _, err := config.Parse([]string{"-gap", gap, "/src"}); err == nil {
			t.Errorf("expected error for -gap %s", gap)
		}
	}
}

func TestParseHome(t *testing.T) {
	cfg, err := config.Parse([]string{"-home", "45.188,5.724", "/src"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Home == nil || cfg.Home.Lat != 45.188 || cfg.Home.Lng != 5.724 {
		t.Fatalf("Home = %+v; want {45.188, 5.724}", cfg.Home)
	}
}

func TestParseHomeUnparsable(t *testing.T) {
	for _, h := range []string{"not-coords", "45.0", "a,b", "1,2,3"} {
		if _, err := config.Parse([]string{"-home", h, "/src"}); err == nil {
			t.Errorf("expected error for -home %q", h)
		}
	}
}

func TestValidateSourceMissing(t *testing.T) {
	cfg, err := config.Parse([]string{"/definitely/does/not/exist/xyz"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected Validate error for non-existent source")
	}
}

func TestValidateSourceIsFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "afile.txt")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse([]string{f})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected Validate error when source is a file, not a directory")
	}
}

func TestValidateOK(t *testing.T) {
	dir := t.TempDir()
	cfg, err := config.Parse([]string{dir})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !strings.HasSuffix(cfg.DestRoot, "_trie") {
		t.Errorf("DestRoot = %q; want suffix _trie", cfg.DestRoot)
	}
}
