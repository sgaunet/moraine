// Package config centralises moraine's typed configuration (Constitution
// Principle II): a single Config struct built once from CLI flags, validated,
// then passed explicitly to the packages that need it. No mutable globals.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
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
	ShowVersion   bool          // -version: print version and exit before requiring a source
}

// ErrHelp is returned by Parse when -h/-help is requested. Callers detect it with
// errors.Is and render WriteUsage to stdout, exiting 0 (help is not an error).
var ErrHelp = flag.ErrHelp

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

// cliFlags holds the pointers returned by flag registration so that Parse and
// WriteUsage share a single flag definition (no drift in names, defaults, or help).
type cliFlags struct {
	dest, model, ollama, themes, fallback, logLevel, exiftool *string
	gap                                                       *time.Duration
	sample                                                    *int
	version                                                   *bool
}

// registerFlags declares moraine's flags on fs. It is the single source of truth
// for flag names, descriptions, and defaults; WriteUsage reuses it via PrintDefaults.
func registerFlags(fs *flag.FlagSet) *cliFlags {
	return &cliFlags{
		dest:     fs.String("dest", "", "destination root (default <source>/_sorted; excluded from the scan)"),
		model:    fs.String("model", DefaultModel, "Ollama vision model"),
		gap:      fs.Duration("gap", DefaultGap, "max time gap within an event (e.g. 30m, 2h)"),
		sample:   fs.Int("sample", DefaultSample, "photos sampled per large group (0 disables the model)"),
		ollama:   fs.String("ollama-url", DefaultOllamaURL, "base URL of the local Ollama API"),
		themes:   fs.String("themes", DefaultThemes, "themes ([a-z0-9-] slugs, comma-separated)"),
		fallback: fs.String("fallback-theme", DefaultFallback, "fallback theme when none is determined"),
		logLevel: fs.String("log-level", DefaultLogLevel, "log verbosity: debug|info|warn|error"),
		exiftool: fs.String("exiftool", DefaultExifTool, "exiftool executable (name on PATH or absolute path); required to read RAW files"),
		version:  fs.Bool("version", false, "print the version and exit"),
	}
}

// Parse builds a Config from CLI-style arguments (without the program name).
// It reports usage/argument errors (exit code 2 at the call site), and returns
// ErrHelp when -h/-help is requested. Filesystem existence checks and the
// destination-default resolution are deferred to Validate.
func Parse(args []string) (Config, error) {
	fs := flag.NewFlagSet("moraine", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // caller renders usage; avoid flag's own noise

	f := registerFlags(fs)

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return Config{}, ErrHelp // caller prints WriteUsage and exits 0
		}
		return Config{}, fmt.Errorf("invalid arguments: %w", err)
	}

	// -version short-circuits before requiring a source argument.
	if *f.version {
		return Config{ShowVersion: true}, nil
	}

	dest, model, gap := f.dest, f.model, f.gap
	sample, ollama := f.sample, f.ollama
	themes, fallback, logLevel := f.themes, f.fallback, f.logLevel

	rest := fs.Args()
	if len(rest) == 0 {
		return Config{}, errors.New("missing source: a directory or a file is expected")
	}
	if len(rest) > 1 {
		return Config{}, fmt.Errorf("exactly one source is expected, got %d (%v)", len(rest), rest)
	}

	if *gap <= 0 {
		return Config{}, fmt.Errorf("-gap must be strictly positive (got %s)", *gap)
	}
	if *sample < 0 {
		return Config{}, fmt.Errorf("-sample must be zero or positive (got %d)", *sample)
	}

	level, err := parseLevel(*logLevel)
	if err != nil {
		return Config{}, err
	}

	themeList, err := parseThemes(*themes, *fallback)
	if err != nil {
		return Config{}, err
	}

	source, err := filepath.Abs(rest[0])
	if err != nil {
		return Config{}, fmt.Errorf("unreadable source %q: %w", rest[0], err)
	}

	destRoot := ""
	if strings.TrimSpace(*dest) != "" {
		destRoot, err = filepath.Abs(*dest)
		if err != nil {
			return Config{}, fmt.Errorf("unreadable destination directory %q: %w", *dest, err)
		}
	}

	exiftool := strings.TrimSpace(*f.exiftool)
	if exiftool == "" {
		exiftool = DefaultExifTool
	}

	return Config{
		Source:        source,
		DestRoot:      destRoot,
		Model:         *model,
		Gap:           *gap,
		Sample:        *sample,
		OllamaURL:     *ollama,
		Themes:        themeList,
		FallbackTheme: strings.TrimSpace(*fallback),
		ExifToolPath:  exiftool,
		LogLevel:      level,
	}, nil
}

// WriteUsage prints the detailed help screen (description, options, classification
// summary, destination layout, exit codes, and examples) to w. It is rendered on
// -h/-help (to stdout, exit 0) and may be reused on usage errors.
func WriteUsage(w io.Writer) {
	// Write errors to the help writer (stdout) are not actionable; ignore them.
	_, _ = fmt.Fprint(w, `moraine — automatic photo organizer.

Analyzes the photos in a directory (or a single photo), groups them into events
by capture time, assigns a theme to each group, then COPIES each photo to
destination/<theme>/<year>/<year-month-day>/<name>. Originals are never modified
or deleted.

Usage:
  moraine [options] <directory-or-file>
  moraine clean [options] <source-dir>   (delete originals already copied; see `+"`moraine clean -help`"+`)

Argument:
  <directory-or-file>   directory (batch mode) or file (single photo)

Options:
`)
	// Reuse the real flag definitions so names/defaults/descriptions never drift.
	fs := flag.NewFlagSet("moraine", flag.ContinueOnError)
	fs.SetOutput(w)
	registerFlags(fs)
	fs.PrintDefaults()

	_, _ = fmt.Fprintf(w, `
Classification (a theme is always assigned):
  1. heuristic: EXIF altitude ≥ 1500 m → "mountain" (no model call);
  2. otherwise, if -sample > 0: the Ollama vision model picks among the themes
     (a group of ≤ 3 photos is sent whole, otherwise a sample of -sample photos);
  3. otherwise, or on failure/out-of-list answer: the fallback theme (-fallback-theme).
  HEIC photos are dated and organized but not sent to the model (no pure-Go HEIC
  decoding): a HEIC-only group follows the heuristic or the fallback.
  RAW photos (.dng/.nef/.cr2/…) are organized too; their embedded preview is
  extracted with exiftool (required, see -exiftool) and sent to the model.

Destination:
  <dest>/<theme>/<year>/<year-month-day>/<name>
  e.g. ~/Photos/sorted/nature/2025/2025-08-12/IMG_1234.jpg
  Default themes: %s (fallback: %s).

Exit codes:
  0  success        1  runtime error        2  usage error

Examples:
  moraine -dest ~/Photos/sorted ~/Photos/2025                  # organize a directory
  moraine -dest ~/Photos/sorted ~/Photos/2025/IMG_1234.jpg     # a single photo
  moraine -sample 0 -dest ~/Photos/sorted ~/Photos/2025        # without Ollama (heuristic + fallback)
  moraine -themes "friends,hiking,party,nature" -fallback-theme "misc" \
    -log-level debug -dest ~/Photos/sorted ~/Photos/2025       # custom vocabulary + verbose logs
`, DefaultThemes, DefaultFallback)
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
		return nil, fmt.Errorf("invalid -fallback-theme %q: expected [a-z0-9-]", fallback)
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
		return nil, errors.New("-themes must not be empty")
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
		return 0, fmt.Errorf("-log-level invalid %q: expected debug|info|warn|error", s)
	}
}
