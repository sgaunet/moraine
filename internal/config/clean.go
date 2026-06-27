package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// CleanConfig holds the typed configuration for one `clean` invocation. It is built
// once by NewClean (syntax/cross-field, no I/O) and finalised by Validate (filesystem).
type CleanConfig struct {
	Source   string     // absolute path of the source tree to clean
	DestRoot string     // absolute path of the destination library (the "already archived" set)
	Delete   bool       // false ⇒ dry-run (report only); true ⇒ actually delete matched originals
	LogLevel slog.Level // logging verbosity
}

// CleanOptions carries the already-parsed CLI inputs for a clean run. The transport
// layer fills it from typed flags and a single positional Source, then calls NewClean.
type CleanOptions struct {
	Source   string // positional argument (source directory)
	Dest     string // --dest (empty ⇒ resolved to <source>/_sorted in Validate)
	Delete   bool   // --delete
	LogLevel string // --log-level (textual)
}

// NewClean builds a validated CleanConfig from already-parsed CLI Options. It
// performs syntax/cross-field checks only (an invalid log-level, an unreadable
// path) — these map to a usage error (exit 2) at the call site. Filesystem checks
// are deferred to Validate.
func NewClean(o CleanOptions) (CleanConfig, error) {
	level, err := parseLevel(o.LogLevel)
	if err != nil {
		return CleanConfig{}, err
	}

	source, err := filepath.Abs(o.Source)
	if err != nil {
		return CleanConfig{}, fmt.Errorf("unreadable source %q: %w", o.Source, err)
	}

	destRoot := ""
	if strings.TrimSpace(o.Dest) != "" {
		destRoot, err = filepath.Abs(o.Dest)
		if err != nil {
			return CleanConfig{}, fmt.Errorf("unreadable destination directory %q: %w", o.Dest, err)
		}
	}

	return CleanConfig{
		Source:   source,
		DestRoot: destRoot,
		Delete:   o.Delete,
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
			return fmt.Errorf("destination %q does not exist; run the sort first or pass --dest", c.DestRoot)
		}
		return fmt.Errorf("destination %q is not readable: %w", c.DestRoot, err)
	}
	if !dinfo.IsDir() {
		return fmt.Errorf("destination %q must be a directory", c.DestRoot)
	}
	return nil
}
