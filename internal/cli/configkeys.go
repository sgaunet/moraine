package cli

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"go.yaml.in/yaml/v3"

	"github.com/sgaunet/moraine/internal/config"
	"github.com/sgaunet/moraine/internal/configfile"
)

// This file is the single description of what a configuration file may hold. It
// drives `config show`, `set`, `unset` and `edit` alike, so a setting is described
// once rather than four times.
//
// Two things it deliberately does NOT hold:
//
//   - Flag defaults. Those are read back from the real sort/clean/undo commands, so
//     `config show` reports exactly what `--help` prints — including the defaults
//     that are literals at the flag rather than config.Default* constants.
//   - Help text. The flag's own usage string is reused, so the form and the flag
//     cannot come to describe the same setting differently.
//
// The mode flags are absent on purpose: --dry-run, --delete, --incremental, --quiet,
// --verbose and --move choose what a single invocation does, and the file does not
// get a say in that (Constitution Principle V). configkeys_test.go asserts that the
// only flags missing from this table are those.

// The sections of a configuration file. Each names the command it configures, except
// sectionShared, which is the top level whose settings every command inherits.
const (
	sectionShared = "shared"
	sectionSort   = "sort"
	sectionClean  = "clean"
	sectionUndo   = "undo"
)

// sections lists them in the order `config show` reports them.
var sections = []string{sectionShared, sectionSort, sectionClean, sectionUndo}

// unconfigurable names the flags a configuration file deliberately cannot set. They
// choose what a single invocation does, and Constitution Principle V exists so that a
// mistyped command cannot delete anything: a file that made `clean` destructive by
// default, or every `sort` a silent no-op, would subvert exactly that. --quiet and
// --verbose are excluded for a different reason — they are shorthands over log-level,
// whose mutual exclusivity cobra can only enforce for flags typed on the command line.
//
// It is a list rather than a comment because configkeys_test.go reads it: any flag of
// sort, clean or undo that is neither settable nor named here fails the suite, so a
// new flag cannot quietly become unconfigurable.
var unconfigurable = []string{"dry-run", "delete", "incremental", "move", "quiet", "verbose"}

// settingKind is how a setting is spelled in YAML and offered in the form.
type settingKind int

const (
	kindString settingKind = iota
	kindBool
	kindInt
	kindFloat
	kindDuration
	kindList
)

// setting describes one configurable value. Flag is the name it has on the command
// line, YAML the key it has in the file — the same word in snake_case, which is the
// convention .golangci.yml's tagliatelle settings pin.
type setting struct {
	Flag    string
	YAML    string
	Kind    settingKind
	Choices []string // a closed value set, offered as a list in the form
}

// sharedSettings are the settings every command accepts. They may be written at the
// top level and overridden inside a command's section.
var sharedSettings = []setting{
	{Flag: "log-level", YAML: "log_level", Kind: kindString, Choices: logLevels},
	{Flag: "output", YAML: "output", Kind: kindString, Choices: outputFormats},
	{Flag: "dest", YAML: "dest", Kind: kindString},
}

// sortOnlySettings are the settings only `sort` has.
var sortOnlySettings = []setting{
	{Flag: "gap", YAML: "gap", Kind: kindDuration},
	{Flag: "path-template", YAML: "path_template", Kind: kindString},
	{Flag: "themes", YAML: "themes", Kind: kindList},
	{Flag: "fallback-theme", YAML: "fallback_theme", Kind: kindString},
	{Flag: "sidecars", YAML: "sidecars", Kind: kindBool},
	{Flag: "model", YAML: "model", Kind: kindString},
	{Flag: "ollama-url", YAML: "ollama_url", Kind: kindString},
	{Flag: "sample", YAML: "sample", Kind: kindInt},
	{Flag: "min-confidence", YAML: "min_confidence", Kind: kindFloat},
	{Flag: "vote", YAML: "vote", Kind: kindBool},
	{Flag: "mountain-altitude", YAML: "mountain_altitude", Kind: kindFloat},
	{Flag: "jobs", YAML: "jobs", Kind: kindInt},
	{Flag: "exiftool", YAML: "exiftool", Kind: kindString},
}

// undoSettings are the settings `undo` accepts. It takes its destination as an
// argument, so there is no dest to configure.
var undoSettings = []setting{
	{Flag: "log-level", YAML: "log_level", Kind: kindString, Choices: logLevels},
	{Flag: "output", YAML: "output", Kind: kindString, Choices: outputFormats},
}

// settingsFor returns the settings a section accepts, in the order the form and
// `config show` present them.
func settingsFor(section string) []setting {
	switch section {
	case sectionSort:
		return slices.Concat(sharedSettings, sortOnlySettings)
	case sectionUndo:
		return undoSettings
	case sectionShared, sectionClean:
		return sharedSettings
	default:
		return nil
	}
}

// lookupSetting finds a setting of a section by either of its names, so a user may
// unset "path-template" or "path_template" and mean the same thing.
func lookupSetting(section, name string) (setting, bool) {
	for _, s := range settingsFor(section) {
		if s.Flag == name || s.YAML == name {
			return s, true
		}
	}
	return setting{}, false
}

// key is how a setting is addressed on stdout: the YAML key, prefixed by its section
// unless it is a top-level one.
func (s setting) key(section string) string {
	if section == sectionShared {
		return s.YAML
	}
	return section + "." + s.YAML
}

// path is where the setting lives in the file's node tree.
func (s setting) path(section string) []string {
	if section == sectionShared {
		return []string{s.YAML}
	}
	return []string{section, s.YAML}
}

// referenceFlags returns the flags of the real command a section configures, which is
// where both the defaults and the help text come from. Registering them is enough:
// pflag writes each default into the variable it binds, so nothing has to be run.
//
// The shared settings borrow sort's: --dest and --log-level are its own flags, and
// they mean the same thing wherever they are written.
func referenceFlags(section string) *pflag.FlagSet {
	f := pflag.NewFlagSet(section, pflag.ContinueOnError)
	switch section {
	case sectionClean:
		var opts config.CleanOptions
		registerCleanFlags(f, &opts)
	case sectionUndo:
		var opts config.UndoOptions
		registerUndoFlags(f, &opts)
	default:
		var opts config.Options
		registerSortFlags(f, &opts)
	}
	return f
}

// defaultSortOptions returns the options a `sort` run starts from: every flag at the
// default its registration gives it. `config` uses it to check a candidate
// configuration file exactly the way sort would read it, with no second copy of the
// defaults to fall out of step.
func defaultSortOptions() config.Options {
	var opts config.Options
	registerSortFlags(pflag.NewFlagSet(sectionSort, pflag.ContinueOnError), &opts)
	opts.Output = config.DefaultOutput // persistent on the root command, not on sort
	return opts
}

// defaultCleanOptions returns the options a `clean` run starts from.
func defaultCleanOptions() config.CleanOptions {
	var opts config.CleanOptions
	registerCleanFlags(pflag.NewFlagSet(sectionClean, pflag.ContinueOnError), &opts)
	opts.Output = config.DefaultOutput
	return opts
}

// defaultUndoOptions returns the options an `undo` run starts from.
func defaultUndoOptions() config.UndoOptions {
	var opts config.UndoOptions
	registerUndoFlags(pflag.NewFlagSet(sectionUndo, pflag.ContinueOnError), &opts)
	opts.Output = config.DefaultOutput
	return opts
}

// describe returns a setting's default value and its help text, as `--help` states
// them. --output is the one setting with no subcommand flag to read: it is
// persistent on the root command, so its default and usage are named here.
func describe(section string, s setting) (defaultValue, help string) {
	if s.Flag == "output" {
		return config.DefaultOutput, "stdout format for the run result: text|json (logs always go to stderr)"
	}
	f := referenceFlags(section).Lookup(s.Flag)
	if f == nil {
		return "", ""
	}
	if s.Kind == kindDuration {
		return prettyDuration(f.DefValue), f.Usage
	}
	return f.DefValue, f.Usage
}

// fileValue returns the value a file gives a setting, spelled the way the flag would
// spell it, and whether the file sets it at all.
//
// The section accessors resolve inheritance, so a top-level "output: json" reads as
// set for sort, clean and undo alike — which is what "the value this command will
// actually use" means.
func fileValue(f *configfile.File, section, yamlKey string) (string, bool) {
	if f == nil {
		return "", false // no configuration file: every setting is at its default
	}
	switch section {
	case sectionShared:
		return sharedValue(f.Shared, yamlKey)
	case sectionSort:
		return sortValue(f.SortSection(), yamlKey)
	case sectionClean:
		return sharedValue(f.CleanSection().Shared, yamlKey)
	case sectionUndo:
		u := f.UndoSection()
		return sharedValue(configfile.Shared{LogLevel: u.LogLevel, Output: u.Output}, yamlKey)
	default:
		return "", false
	}
}

// sharedValue reads one of the three settings every section has.
func sharedValue(s configfile.Shared, key string) (string, bool) {
	switch key {
	case "log_level":
		return derefString(s.LogLevel)
	case "output":
		return derefString(s.Output)
	case "dest":
		return derefString(s.Dest)
	default:
		return "", false
	}
}

// sortValue reads one of sort's settings, falling back to the shared three.
func sortValue(s configfile.Sort, key string) (string, bool) {
	if v, ok := sharedValue(s.Shared, key); ok {
		return v, true
	}
	switch key {
	case "model":
		return derefString(s.Model)
	case "gap":
		if s.Gap == nil {
			return "", false
		}
		return prettyDuration(s.Gap.String()), true
	case "sample":
		return derefInt(s.Sample)
	case "ollama_url":
		return derefString(s.OllamaURL)
	case "themes":
		if s.Themes == nil {
			return "", false
		}
		return strings.Join(s.Themes, ","), true
	case "fallback_theme":
		return derefString(s.FallbackTheme)
	case "path_template":
		return derefString(s.PathTemplate)
	case "exiftool":
		return derefString(s.ExifTool)
	case "sidecars":
		return derefBool(s.Sidecars)
	case "jobs":
		return derefInt(s.Jobs)
	case "mountain_altitude":
		return derefFloat(s.MountainAltitude)
	case "min_confidence":
		return derefFloat(s.MinConfidence)
	case "vote":
		return derefBool(s.Vote)
	default:
		return "", false
	}
}

// The four pointer readers are spelled out rather than made generic: each formats its
// value differently, so a type parameter would move the switch rather than remove it
// (Constitution Principle IV).

func derefString(p *string) (string, bool) {
	if p == nil {
		return "", false
	}
	return *p, true
}

func derefInt(p *int) (string, bool) {
	if p == nil {
		return "", false
	}
	return strconv.Itoa(*p), true
}

func derefFloat(p *float64) (string, bool) {
	if p == nil {
		return "", false
	}
	return strconv.FormatFloat(*p, 'g', -1, 64), true
}

func derefBool(p *bool) (string, bool) {
	if p == nil {
		return "", false
	}
	return strconv.FormatBool(*p), true
}

// valueNode builds the YAML node a setting's value is written as. Every tag is set
// explicitly rather than inferred from the text: a theme named "true" or a template
// spelled "null" would otherwise be written unquoted and read back as something else
// entirely.
func valueNode(s setting, raw string) *yaml.Node {
	scalarNode := func(tag, value string) *yaml.Node {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
	}
	switch s.Kind {
	case kindBool:
		return scalarNode("!!bool", raw)
	case kindInt:
		return scalarNode("!!int", raw)
	case kindFloat:
		return scalarNode("!!float", raw)
	case kindDuration:
		// A duration is a string in the file ("6h"), which is what its
		// UnmarshalYAML expects and what the README documents.
		return scalarNode("!!str", prettyDuration(raw))
	case kindList:
		// A flow sequence keeps the [mountain, cook] shape the README shows.
		items := make([]*yaml.Node, 0)
		for _, v := range strings.Split(raw, ",") {
			if v = strings.TrimSpace(v); v != "" {
				items = append(items, scalarNode("!!str", v))
			}
		}
		return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle, Content: items}
	case kindString:
		return scalarNode("!!str", raw)
	default:
		return scalarNode("!!str", raw)
	}
}

// prettyDuration rewrites a Go duration in the shortest form that parses back to the
// same value, so a file records "6h" rather than the "6h0m0s" time.Duration.String
// produces and the README documents. Anything that is not a duration — every other
// kind of setting reaching valueNode — is passed through untouched.
//
// It is arithmetic rather than suffix-trimming on purpose: "10s" and "30m" both end
// in the text a naive trim would cut.
func prettyDuration(raw string) string {
	d, err := time.ParseDuration(raw)
	if err != nil {
		return raw
	}
	if d <= 0 || d%time.Minute != 0 {
		return d.String() // sub-minute precision: leave Go's own spelling alone
	}
	hours, minutes := int64(d/time.Hour), int64((d%time.Hour)/time.Minute)
	switch {
	case hours == 0:
		return strconv.FormatInt(minutes, 10) + "m"
	case minutes == 0:
		return strconv.FormatInt(hours, 10) + "h"
	default:
		return strconv.FormatInt(hours, 10) + "h" + strconv.FormatInt(minutes, 10) + "m"
	}
}
