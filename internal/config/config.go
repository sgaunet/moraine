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
	Source           string        // absolute path of the source (a directory → batch, a file → single photo)
	SourceIsDir      bool          // resolved by Validate: directory (batch) vs regular file (single)
	DestRoot         string        // absolute path of the copy destination root (excluded from scan)
	Model            string        // Ollama vision model
	Gap              time.Duration // max temporal gap within an event
	Sample           int           // photos sampled per large group for the model (0 disables the model stage)
	OllamaURL        string        // base URL of the local Ollama API
	Themes           []string      // configured theme slugs (folder names)
	FallbackTheme    string        // theme slug used when none is confidently chosen
	ExifToolPath     string        // exiftool executable (name on PATH or absolute path)
	LogLevel         slog.Level    // logging verbosity
	Output           OutputFormat  // stdout rendering of the run result (text | json)
	Sidecars         bool          // copy each photo's companion (sidecar) files alongside it
	DryRun           bool          // report the planned placements without writing anything
	Incremental      bool          // trust the run manifest to skip sources already placed
	Jobs             int           // EXIF worker count (0 ⇒ one per GOMAXPROCS)
	MountainAltitude float64       // metres at/above which the heuristic labels a group "mountain" (always > 0)
	MinConfidence    float64       // confidence a model verdict must reach to be used (0 ⇒ accept every verdict)
	Vote             bool          // classify each sampled photo separately and let them vote
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
	DefaultOutput    = "text"
	// DefaultMountainAltitude matches the documented heuristic threshold
	// (specs/002-auto-photo-organizer: altitude >= 1500m -> mountain).
	DefaultMountainAltitude = 1500.0
)

// OutputFormat selects how a run's result is rendered on stdout. Stdout carries
// data only (Constitution Principle V), so this is the one knob that shapes the
// tool's machine-facing contract; logs and progress always go to stderr regardless.
type OutputFormat string

// The supported stdout renderings.
const (
	// OutputText renders the run summary as a single key=value line.
	OutputText OutputFormat = "text"
	// OutputJSON renders one JSON object holding every per-file record and the summary.
	OutputJSON OutputFormat = "json"
)

// slugPattern constrains theme slugs to filesystem-safe lowercase tokens.
var slugPattern = regexp.MustCompile(`^[a-z0-9-]+$`)

// Options carries the already-parsed CLI inputs for an organize run. The transport
// layer fills it from typed flags (Gap/Sample arrive typed; the rest as strings)
// and a single positional Source, then calls New.
type Options struct {
	Source           string        // positional argument (directory or file)
	Dest             string        // --dest (empty ⇒ resolved to <source>/_sorted in Validate)
	Model            string        // --model
	Gap              time.Duration // --gap
	Sample           int           // --sample
	OllamaURL        string        // --ollama-url
	Themes           string        // --themes (comma-separated slug list)
	Fallback         string        // --fallback-theme
	LogLevel         string        // --log-level (textual)
	Quiet            bool          // --quiet (errors only; excludes --verbose/--log-level)
	Verbose          bool          // --verbose (per-file detail; excludes --quiet/--log-level)
	Output           string        // --output (textual: text|json)
	ExifTool         string        // --exiftool
	Sidecars         bool          // --sidecars (copy companion files; default true at the flag)
	DryRun           bool          // --dry-run (report the plan, write nothing)
	Incremental      bool          // --incremental (skip sources the manifest already records as placed)
	Jobs             int           // --jobs (EXIF workers; 0 ⇒ one per GOMAXPROCS)
	MountainAltitude float64       // --mountain-altitude (metres; must be > 0)
	MinConfidence    float64       // --min-confidence (0..1; 0 disables the gate)
	Vote             bool          // --vote (per-photo classification + majority vote)
}

// New builds a validated Config from already-parsed CLI Options. It performs
// syntax / cross-field checks only (a non-positive gap or mountain altitude, a
// negative sample or job count, a confidence threshold outside 0..1, an invalid
// theme/fallback/log-level/output, an unreadable path) — these map to a usage error
// (exit 2) at the call site.
// Filesystem existence checks and the destination-default resolution are deferred
// to Validate.
func New(o Options) (Config, error) {
	if o.Gap <= 0 {
		return Config{}, fmt.Errorf("--gap must be strictly positive (got %s)", o.Gap)
	}
	if o.Sample < 0 {
		return Config{}, fmt.Errorf("--sample must be zero or positive (got %d)", o.Sample)
	}
	if o.MountainAltitude <= 0 {
		return Config{}, fmt.Errorf("--mountain-altitude must be strictly positive (got %g)", o.MountainAltitude)
	}
	if o.Jobs < 0 {
		return Config{}, fmt.Errorf("--jobs must be zero or positive (got %d)", o.Jobs)
	}
	if o.MinConfidence < 0 || o.MinConfidence > 1 {
		return Config{}, fmt.Errorf("--min-confidence must be between 0 and 1 (got %g)", o.MinConfidence)
	}

	level, err := resolveLevel(o.LogLevel, o.Quiet, o.Verbose)
	if err != nil {
		return Config{}, err
	}

	output, err := ParseOutput(o.Output)
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
		Source:           source,
		DestRoot:         destRoot,
		Model:            o.Model,
		Gap:              o.Gap,
		Sample:           o.Sample,
		OllamaURL:        o.OllamaURL,
		Themes:           themeList,
		FallbackTheme:    strings.TrimSpace(o.Fallback),
		ExifToolPath:     exiftool,
		LogLevel:         level,
		Output:           output,
		Sidecars:         o.Sidecars,
		DryRun:           o.DryRun,
		Incremental:      o.Incremental,
		Jobs:             o.Jobs,
		MountainAltitude: o.MountainAltitude,
		MinConfidence:    o.MinConfidence,
		Vote:             o.Vote,
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

// ParseOutput maps a textual --output value to an OutputFormat. It is exported
// because `version` renders stdout without building a full Config, and the flag must
// mean the same thing for every command.
func ParseOutput(s string) (OutputFormat, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(OutputText):
		return OutputText, nil
	case string(OutputJSON):
		return OutputJSON, nil
	default:
		return "", fmt.Errorf("--output invalid %q: expected text|json", s)
	}
}

// resolveLevel turns the mutually exclusive verbosity flags into one slog.Level.
// The transport enforces the exclusivity (cobra's MarkFlagsMutuallyExclusive), so
// at most one of quiet/verbose/--log-level can have been set.
func resolveLevel(logLevel string, quiet, verbose bool) (slog.Level, error) {
	switch {
	case quiet:
		return slog.LevelError, nil
	case verbose:
		return slog.LevelDebug, nil
	default:
		return parseLevel(logLevel)
	}
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
