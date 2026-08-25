// Package configfile reads moraine's optional YAML configuration file. It decodes
// and nothing more: turning a file plus a set of command-line flags into a validated
// run is the transport's job (internal/cli), and turning already-parsed values into
// a typed Config is internal/config's.
//
// Every setting is a pointer (or, for a list, a nil-able slice), so "absent from the
// file" is distinguishable from "present and equal to the default". That is what lets
// a flag win over a file without this package knowing a single flag default.
//
// The write half lives in document.go, and is what `moraine config` edits a file
// with; it goes through the YAML node tree so that a user's comments survive.
//
// Note what is deliberately *not* configurable: --dry-run, --delete, --incremental,
// --move and the positional source. Those select what a single invocation does, and
// Constitution Principle V exists so that "a mistyped command cannot delete
// anything" — a file that turns `clean` destructive by default would subvert exactly
// that, and a file that made every `sort` a silent no-op would be worse. --quiet and
// --verbose are excluded for a different reason: they are shorthands over log_level
// whose mutual exclusivity cobra can only enforce for flags typed on the command
// line, so a file sets log_level directly and no file value can ever silently beat a
// flag the user typed.
package configfile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"go.yaml.in/yaml/v3"
)

// EnvVar names the file to read, overriding the default locations. Setting it to the
// empty string disables the configuration file entirely, which is both a user's
// escape hatch and how the test suites keep a developer's real file out of the way.
const EnvVar = "MORAINE_CONFIG"

// baseName is the file's name in whichever configuration directory holds it.
const baseName = "moraine.yaml"

// Duration is a time.Duration written the way the flag is: a string such as "6h".
// yaml would otherwise insist on a bare nanosecond count.
type Duration struct {
	time.Duration
}

// UnmarshalYAML decodes a Go duration string.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return fmt.Errorf("a duration must be written as a string such as \"6h\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = v
	return nil
}

// Shared holds the settings more than one command accepts. They may be written once
// at the top level and overridden inside a command's section.
type Shared struct {
	LogLevel *string `yaml:"log_level"`
	Output   *string `yaml:"output"`
	Dest     *string `yaml:"dest"`
}

// Sort holds the settings of the sort command.
type Sort struct {
	Shared `yaml:",inline"`

	Model            *string   `yaml:"model"`
	Gap              *Duration `yaml:"gap"`
	Sample           *int      `yaml:"sample"`
	OllamaURL        *string   `yaml:"ollama_url"`
	Themes           []string  `yaml:"themes"`
	FallbackTheme    *string   `yaml:"fallback_theme"`
	PathTemplate     *string   `yaml:"path_template"`
	ExifTool         *string   `yaml:"exiftool"`
	Sidecars         *bool     `yaml:"sidecars"`
	Jobs             *int      `yaml:"jobs"`
	MountainAltitude *float64  `yaml:"mountain_altitude"`
	MinConfidence    *float64  `yaml:"min_confidence"`
	Vote             *bool     `yaml:"vote"`
}

// Clean holds the settings of the clean command.
type Clean struct {
	Shared `yaml:",inline"`
}

// Undo holds the settings of the undo command. It has no dest: undo takes the
// destination root as its argument.
type Undo struct {
	LogLevel *string `yaml:"log_level"`
	Output   *string `yaml:"output"`
}

// File is a decoded configuration file.
type File struct {
	Shared `yaml:",inline"`

	Sort  Sort  `yaml:"sort"`
	Clean Clean `yaml:"clean"`
	Undo  Undo  `yaml:"undo"`
}

// SortSection returns the sort settings with the top-level shared keys filled in
// wherever the section does not override them. A nil File yields an empty section, so
// callers need no nil check of their own.
func (f *File) SortSection() Sort {
	if f == nil {
		return Sort{}
	}
	s := f.Sort
	fillShared(&s.Shared, f.Shared)
	return s
}

// CleanSection returns the clean settings, filled in from the top level.
func (f *File) CleanSection() Clean {
	if f == nil {
		return Clean{}
	}
	c := f.Clean
	fillShared(&c.Shared, f.Shared)
	return c
}

// UndoSection returns the undo settings, filled in from the top level.
func (f *File) UndoSection() Undo {
	if f == nil {
		return Undo{}
	}
	u := f.Undo
	if u.LogLevel == nil {
		u.LogLevel = f.LogLevel
	}
	if u.Output == nil {
		u.Output = f.Output
	}
	return u
}

// fillShared copies the keys src provides that dst has not overridden.
func fillShared(dst *Shared, src Shared) {
	if dst.LogLevel == nil {
		dst.LogLevel = src.LogLevel
	}
	if dst.Output == nil {
		dst.Output = src.Output
	}
	if dst.Dest == nil {
		dst.Dest = src.Dest
	}
}

// Load finds and reads the configuration file for this run. It returns the decoded
// file and the path it came from; both are zero when there is no file to read, which
// is the ordinary case and not an error.
//
// explicit is the --config value. Where a file was named on purpose — by --config or
// by MORAINE_CONFIG — its absence is an error, because the user asked for it. The
// implicit locations are optional by nature: not having a configuration file is how
// most runs work.
func Load(explicit string) (*File, string, error) {
	loc, named, err := candidate(explicit)
	if err != nil {
		// Nowhere to read from is not a failure for a run: it is the same as having
		// no configuration file. Only a writer (Target) needs to report it.
		return nil, "", nil
	}
	path := loc.Path
	f, err := read(path)
	if err != nil {
		if !named && errors.Is(err, fs.ErrNotExist) {
			return nil, "", nil
		}
		return nil, "", err
	}
	return f, path, nil
}

// Location names the configuration file this environment designates, and why that
// one. The reason is worth carrying: "which file is moraine reading?" is the first
// question asked when a setting appears to have done nothing.
type Location struct {
	Path   string
	Source string
}

// The reasons one file rather than another is in effect, in precedence order.
const (
	SourceFlag = "--config"
	SourceEnv  = EnvVar
	SourceXDG  = "XDG_CONFIG_HOME"
	SourceHome = "home"
)

// candidate resolves which file this environment designates, and whether the user
// named it on purpose. The two ways there is no file at all — MORAINE_CONFIG set to
// the empty string, and no home directory to look in — come back as an error, since
// they mean "no configuration" to a reader but "nowhere to write" to a writer, and
// only the writer can say anything useful about it.
//
// This is the single definition of the search order; Load and Target both use it.
func candidate(explicit string) (loc Location, named bool, err error) {
	if explicit != "" {
		return Location{Path: explicit, Source: SourceFlag}, true, nil
	}
	if env, ok := os.LookupEnv(EnvVar); ok {
		if env == "" {
			return Location{}, false, fmt.Errorf(
				"%s is set to the empty string, which turns the configuration file off; "+
					"unset it, or name a file with --config", EnvVar)
		}
		return Location{Path: env, Source: SourceEnv}, true, nil
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return Location{Path: filepath.Join(xdg, baseName), Source: SourceXDG}, false, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// Note os.UserConfigDir is deliberately not used — on macOS it points at
		// ~/Library/Application Support, and moraine documents ~/.config.
		return Location{}, false, fmt.Errorf(
			"no home directory to hold %s (name a file with --config): %w", baseName, err)
	}
	return Location{Path: filepath.Join(home, ".config", baseName), Source: SourceHome}, false, nil
}

// Read decodes the file at path, or reports an empty configuration when there is no
// file there.
//
// It is what `moraine config` reads with. A run is stricter — Load makes a file named
// by --config or MORAINE_CONFIG mandatory, so a typo cannot silently apply nothing —
// but for the config commands "not created yet" is the ordinary state before the
// first `config set`, and the state `config path` reports as exists=false.
func Read(path string) (*File, error) {
	f, err := read(path)
	if err != nil && errors.Is(err, fs.ErrNotExist) {
		return &File{}, nil
	}
	return f, err
}

// read decodes one file.
func read(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file %q: %w", path, err)
	}
	f, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("config file %q: %w", path, err)
	}
	return f, nil
}

// Parse decodes a configuration document held in memory. Decoding is strict: an
// unrecognised key is an error, because a typo that silently does nothing is the
// worst thing a configuration file can do.
//
// It is exported so that `moraine config` can check a file it is about to write with
// exactly the strictness the next run will read it with, rather than a second
// implementation that could disagree.
func Parse(data []byte) (*File, error) {
	var f File
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		if errors.Is(err, io.EOF) {
			return &File{}, nil // an empty file is a valid one that sets nothing
		}
		return nil, err
	}
	return &f, nil
}
