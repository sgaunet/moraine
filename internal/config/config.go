// Package config centralises moraine's typed configuration (Constitution
// Principle II): a single Config struct built once from CLI inputs, validated,
// then passed explicitly to the packages that need it. No mutable globals.
//
// Flag *parsing* lives in the transport layer (internal/cli, via Cobra/pflag);
// this package only turns already-parsed values into a validated, typed Config:
// New performs syntax/cross-field checks (no filesystem I/O) and Validate performs
// the filesystem checks and default-destination resolution.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Config holds all runtime parameters for a moraine organize run.
type Config struct {
	Source        string        // absolute path of the source (a directory → batch, a file → single photo)
	SourceIsDir   bool          // resolved by Validate: directory (batch) vs regular file (single)
	DestRoot      string        // absolute path of the copy destination root (excluded from scan)
	Model         string        // Ollama vision model
	Gap           time.Duration // max temporal gap within an event
	Sample        int           // photos sampled per large group for the model (0 disables the model stage)
	OllamaURL     string        // base URL of the local Ollama API
	Themes        []string      // configured theme slugs (folder names)
	FallbackTheme string        // theme slug used when none is confidently chosen
	ExifToolPath  string        // exiftool executable (name on PATH or absolute path)
	LogLevel      slog.Level    // logging verbosity
}

// Default values surfaced in the CLI contract.
const (
	DefaultModel     = "qwen3-vl:8b"
	DefaultGap       = 6 * time.Hour
	DefaultSample    = 3
	DefaultOllamaURL = "http://127.0.0.1:11434"
	DefaultThemes    = "mountain,special-events,cook,family"
	DefaultFallback  = "other"
	DefaultLogLevel  = "info"
	DefaultDestName  = "_sorted"
	DefaultExifTool  = "exiftool"
)

// slugPattern constrains theme slugs to filesystem-safe lowercase tokens.
var slugPattern = regexp.MustCompile(`^[a-z0-9-]+$`)

// Options carries the already-parsed CLI inputs for an organize run. The transport
// layer fills it from typed flags (Gap/Sample arrive typed; the rest as strings)
// and a single positional Source, then calls New.
type Options struct {
	Source    string        // positional argument (directory or file)
	Dest      string        // --dest (empty ⇒ resolved to <source>/_sorted in Validate)
	Model     string        // --model
	Gap       time.Duration // --gap
	Sample    int           // --sample
	OllamaURL string        // --ollama-url
	Themes    string        // --themes (comma-separated slug list)
	Fallback  string        // --fallback-theme
	LogLevel  string        // --log-level (textual)
	ExifTool  string        // --exiftool
}

// New builds a validated Config from already-parsed CLI Options. It performs
// syntax / cross-field checks only (a non-positive gap, a negative sample, an
// invalid theme/fallback/log-level, an unreadable path) — these map to a usage
// error (exit 2) at the call site. Filesystem existence checks and the
// destination-default resolution are deferred to Validate.
func New(o Options) (Config, error) {
	if o.Gap <= 0 {
		return Config{}, fmt.Errorf("--gap must be strictly positive (got %s)", o.Gap)
	}
	if o.Sample < 0 {
		return Config{}, fmt.Errorf("--sample must be zero or positive (got %d)", o.Sample)
	}

	level, err := parseLevel(o.LogLevel)
	if err != nil {
		return Config{}, err
	}

	themeList, err := parseThemes(o.Themes, o.Fallback)
	if err != nil {
		return Config{}, err
	}

	source, err := filepath.Abs(o.Source)
	if err != nil {
		return Config{}, fmt.Errorf("unreadable source %q: %w", o.Source, err)
	}

	destRoot := ""
	if strings.TrimSpace(o.Dest) != "" {
		destRoot, err = filepath.Abs(o.Dest)
		if err != nil {
			return Config{}, fmt.Errorf("unreadable destination directory %q: %w", o.Dest, err)
		}
	}

	exiftool := strings.TrimSpace(o.ExifTool)
	if exiftool == "" {
		exiftool = DefaultExifTool
	}

	return Config{
		Source:        source,
		DestRoot:      destRoot,
		Model:         o.Model,
		Gap:           o.Gap,
		Sample:        o.Sample,
		OllamaURL:     o.OllamaURL,
		Themes:        themeList,
		FallbackTheme: strings.TrimSpace(o.Fallback),
		ExifToolPath:  exiftool,
		LogLevel:      level,
	}, nil
}

// Validate performs runtime checks (exit code 1 at the call site): the source
// must exist (file or directory) and the destination default is resolved.
func (c *Config) Validate() error {
	info, err := os.Stat(c.Source)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("source %q does not exist", c.Source)
		}
		return fmt.Errorf("source %q is not readable: %w", c.Source, err)
	}
	c.SourceIsDir = info.IsDir()

	if c.DestRoot == "" {
		base := c.Source
		if !c.SourceIsDir {
			base = filepath.Dir(c.Source)
		}
		c.DestRoot = filepath.Join(base, DefaultDestName)
	}
	return nil
}

// parseThemes splits a comma-separated slug list, validating each slug and the
// fallback, and rejecting empties, duplicates, and a fallback that collides
// with a theme.
func parseThemes(list, fallback string) ([]string, error) {
	fallback = strings.TrimSpace(fallback)
	if !slugPattern.MatchString(fallback) {
		return nil, fmt.Errorf("invalid --fallback-theme %q: expected [a-z0-9-]", fallback)
	}
	seen := make(map[string]struct{})
	var themes []string
	for raw := range strings.SplitSeq(list, ",") {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if !slugPattern.MatchString(s) {
			return nil, fmt.Errorf("invalid theme %q: expected [a-z0-9-]", s)
		}
		if _, dup := seen[s]; dup {
			return nil, fmt.Errorf("duplicate theme %q", s)
		}
		if s == fallback {
			return nil, fmt.Errorf("theme %q cannot be the same as the fallback theme", s)
		}
		seen[s] = struct{}{}
		themes = append(themes, s)
	}
	if len(themes) == 0 {
		return nil, errors.New("--themes must not be empty")
	}
	return themes, nil
}

// parseLevel maps a textual level to slog.Level.
func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("--log-level invalid %q: expected debug|info|warn|error", s)
	}
}
