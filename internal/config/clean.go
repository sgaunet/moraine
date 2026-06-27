package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// CleanConfig holds the typed configuration for one `clean` invocation. It is built
// once by ParseClean (syntax/flags, no I/O) and finalised by Validate (filesystem).
type CleanConfig struct {
	Source   string     // absolute path of the source tree to clean
	DestRoot string     // absolute path of the destination library (the "already archived" set)
	Delete   bool       // false ⇒ dry-run (report only); true ⇒ actually delete matched originals
	LogLevel slog.Level // logging verbosity
}

// cleanFlags holds the pointers returned by flag registration so ParseClean and
// WriteCleanUsage share a single flag definition (no drift in names or defaults).
type cleanFlags struct {
	dest, logLevel *string
	del            *bool
}

// registerCleanFlags declares the clean subcommand's flags on fs. It is the single
// source of truth for those flags; WriteCleanUsage reuses it via PrintDefaults.
func registerCleanFlags(fs *flag.FlagSet) *cleanFlags {
	return &cleanFlags{
		dest:     fs.String("dest", "", "destination root holding the copies (default <source>/_sorted; never deleted from)"),
		del:      fs.Bool("delete", false, "actually delete matched originals (default: dry-run, deletes nothing)"),
		logLevel: fs.String("log-level", DefaultLogLevel, "log verbosity: debug|info|warn|error"),
	}
}

// ParseClean builds a CleanConfig from the arguments that follow the `clean`
// subcommand (the program name and the "clean" token already stripped). It reports
// usage/argument errors (exit code 2 at the call site) and returns ErrHelp on
// -h/-help. Filesystem checks are deferred to Validate.
func ParseClean(args []string) (CleanConfig, error) {
	fs := flag.NewFlagSet("moraine clean", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // caller renders usage; avoid flag's own noise
	f := registerCleanFlags(fs)

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return CleanConfig{}, ErrHelp
		}
		return CleanConfig{}, fmt.Errorf("invalid arguments: %w", err)
	}

	rest := fs.Args()
	if len(rest) == 0 {
		return CleanConfig{}, errors.New("missing source: a directory is expected")
	}
	if len(rest) > 1 {
		return CleanConfig{}, fmt.Errorf("exactly one source is expected, got %d (%v)", len(rest), rest)
	}

	level, err := parseLevel(*f.logLevel)
	if err != nil {
		return CleanConfig{}, err
	}

	source, err := filepath.Abs(rest[0])
	if err != nil {
		return CleanConfig{}, fmt.Errorf("unreadable source %q: %w", rest[0], err)
	}

	destRoot := ""
	if strings.TrimSpace(*f.dest) != "" {
		destRoot, err = filepath.Abs(*f.dest)
		if err != nil {
			return CleanConfig{}, fmt.Errorf("unreadable destination directory %q: %w", *f.dest, err)
		}
	}

	return CleanConfig{
		Source:   source,
		DestRoot: destRoot,
		Delete:   *f.del,
		LogLevel: level,
	}, nil
}

// Validate performs runtime checks (exit code 1 at the call site): the source must
// exist and be a directory, the destination default is resolved, and the
// destination must exist (a missing destination is an actionable error, distinct
// from an existing-but-empty one which yields an empty plan).
func (c *CleanConfig) Validate() error {
	info, err := os.Stat(c.Source)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("source %q does not exist", c.Source)
		}
		return fmt.Errorf("source %q is not readable: %w", c.Source, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("source %q must be a directory (clean recursively scans a source tree)", c.Source)
	}

	if c.DestRoot == "" {
		c.DestRoot = filepath.Join(c.Source, DefaultDestName)
	}

	dinfo, err := os.Stat(c.DestRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("destination %q does not exist; run the sort first or pass -dest", c.DestRoot)
		}
		return fmt.Errorf("destination %q is not readable: %w", c.DestRoot, err)
	}
	if !dinfo.IsDir() {
		return fmt.Errorf("destination %q must be a directory", c.DestRoot)
	}
	return nil
}

// WriteCleanUsage prints the clean subcommand's help screen to w. It is rendered on
// -h/-help (to stdout, exit 0) and may be reused on usage errors.
func WriteCleanUsage(w io.Writer) {
	// Write errors to the help writer (stdout) are not actionable; ignore them.
	_, _ = fmt.Fprint(w, `moraine clean — delete source originals already copied to the destination.

Recursively matches each source file against the destination by SHA-256 content
(never by filename) and deletes a source original only when a byte-identical copy
exists under the destination. Non-photo files and anything not safely copied are
left untouched. The default is a DRY RUN: pass -delete to actually remove files.

Usage:
  moraine clean [options] <source-dir>

Argument:
  <source-dir>   directory whose already-copied originals should be cleaned

Options:
`)
	// Reuse the real flag definitions so names/defaults/descriptions never drift.
	fs := flag.NewFlagSet("moraine clean", flag.ContinueOnError)
	fs.SetOutput(w)
	registerCleanFlags(fs)
	fs.PrintDefaults()

	_, _ = fmt.Fprint(w, `
Safety:
  - Dry-run by default; -delete is required to remove anything.
  - Files under the destination tree are never deleted (even nested inside source).
  - On any read/hash/permission error, the original is kept (fail-safe).
  - Only regular files are considered; symlinks and special files are skipped.

Exit codes:
  0  success        1  runtime error        2  usage error

Examples:
  moraine clean -dest ~/Photos/sorted ~/Photos/2025            # preview (deletes nothing)
  moraine clean -delete -dest ~/Photos/sorted ~/Photos/2025    # delete copied originals
`)
}
