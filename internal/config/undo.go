package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// UndoConfig holds the typed configuration for one `undo` invocation. Unlike sort
// and clean it takes no source: an undo is defined entirely by the destination and
// the run manifest found there.
type UndoConfig struct {
	DestRoot string       // absolute path of the destination library to unwind
	Delete   bool         // false ⇒ dry-run (report only); true ⇒ actually remove the copies
	LogLevel slog.Level   // logging verbosity
	Output   OutputFormat // stdout rendering of the run result (text | json)
	Progress ProgressMode // when stderr is drawn as bullets and progress bars (auto | always | never)
}

// UndoOptions carries the already-parsed CLI inputs for an undo run. The transport
// layer fills it from typed flags and the single positional destination root.
type UndoOptions struct {
	Dest     string // positional argument (destination root)
	Delete   bool   // --delete
	LogLevel string // --log-level (textual)
	Quiet    bool   // --quiet (errors only; excludes --verbose/--log-level)
	Verbose  bool   // --verbose (per-file detail; excludes --quiet/--log-level)
	Output   string // --output (textual: text|json)
	Progress string // --progress (textual: auto|always|never)
}

// NewUndo builds a validated UndoConfig from already-parsed CLI Options. It
// performs syntax/cross-field checks only (an invalid log-level or output format,
// an unreadable path) — these map to a usage error (exit 2) at the call site.
// Filesystem checks are deferred to Validate.
func NewUndo(o UndoOptions) (UndoConfig, error) {
	level, err := resolveLevel(o.LogLevel, o.Quiet, o.Verbose)
	if err != nil {
		return UndoConfig{}, err
	}

	output, err := ParseOutput(o.Output)
	if err != nil {
		return UndoConfig{}, err
	}

	progress, err := ParseProgress(o.Progress)
	if err != nil {
		return UndoConfig{}, err
	}

	if strings.TrimSpace(o.Dest) == "" {
		return UndoConfig{}, fmt.Errorf("a destination root is required (got %q)", o.Dest)
	}
	destRoot, err := filepath.Abs(o.Dest)
	if err != nil {
		return UndoConfig{}, fmt.Errorf("unreadable destination directory %q: %w", o.Dest, err)
	}

	return UndoConfig{
		DestRoot: destRoot, Delete: o.Delete, LogLevel: level, Output: output, Progress: progress,
	}, nil
}

// Validate performs runtime checks (exit code 1 at the call site): the destination
// must exist and be a directory. Whether it holds a run manifest is left to the
// run itself, which can then name the destination it looked in.
func (c *UndoConfig) Validate() error {
	info, err := os.Stat(c.DestRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("destination %q does not exist", c.DestRoot)
		}
		return fmt.Errorf("destination %q is not readable: %w", c.DestRoot, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("destination %q must be a directory", c.DestRoot)
	}
	return nil
}
