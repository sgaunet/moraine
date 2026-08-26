package cli

import (
	"fmt"
	"io"
	"maps"
	"os"
	"slices"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/sgaunet/moraine/internal/config"
	"github.com/sgaunet/moraine/internal/configfile"
)

// newConfigCmd builds the `config` command tree, which is the only part of moraine
// that writes to the configuration file rather than reading it.
//
// The tree carries no run of its own: invoked bare it prints help (exit 0).
func newConfigCmd(stdout, stderr io.Writer, output, configPath *string) *cobra.Command {
	env := configEnv{stdout: stdout, stderr: stderr, output: output, configPath: configPath}
	cmd := &cobra.Command{
		Use:   "config",
		Short: "View and update the configuration file",
		Long: `View and update moraine's configuration file — the layer between the flags you type
and the built-in defaults. Precedence is unchanged and unchangeable here: a flag on
the command line always beats the file, and the file always beats the default.

Which file is written is resolved exactly as a run resolves the file it reads: the one
named by --config, else $MORAINE_CONFIG, else $XDG_CONFIG_HOME/moraine.yaml, else
~/.config/moraine.yaml. "moraine config path" reports which one that is.

Sections mirror the commands. Settings written at the top level (the "shared" section)
apply to every command; a command's own section overrides them for that command.
  shared   log-level, output, dest — inherited by sort, clean and undo
  sort     the shared three plus everything else sort accepts
  clean    the shared three
  undo     log-level and output; undo takes its destination as an argument

Mode flags are deliberately not configurable: --dry-run, --delete, --incremental,
--move, --quiet and --verbose choose what a single invocation does, and a file that
could turn "clean" destructive by default would defeat the reason clean is a dry run
until asked (Constitution Principle V). --quiet and --verbose are shorthands over
log-level, so set log-level itself.

Comments are kept. Editing goes through the file's YAML structure rather than
rewriting it, so comments, key order and any key moraine did not touch all survive.
Blank-line spacing and indentation are normalised the first time moraine writes the
file, and stable from then on.

Commands:
  show     print the effective settings and where each came from
  path     print which configuration file is in effect
  set      write settings, one flag per setting
  unset    remove settings, so they fall back to the default
  edit     fill in a form, prefilled with the values in effect

Output:
  stdout carries the settings only, as one key=value line each (--output=text, the
  default) or one JSON object (--output=json). Logs and errors go to stderr.

Exit codes:
  0  success
  1  runtime failure (nowhere to write, an unwritable file, an aborted form)
  2  usage error (unknown flag or setting, a value no run could use)`,
		Example: `  # what will moraine actually do?
  moraine config show
  moraine config show sort --output=json

  # write a couple of settings
  moraine config set sort --gap 8h --jobs 4
  moraine config set shared --log-level warn

  # take one back
  moraine config unset sort gap

  # fill in a form instead
  moraine config edit sort`,
	}
	cmd.AddCommand(newConfigShowCmd(env))
	cmd.AddCommand(newConfigPathCmd(env))
	cmd.AddCommand(newConfigSetCmd(env))
	cmd.AddCommand(newConfigUnsetCmd(env))
	cmd.AddCommand(newConfigEditCmd(env))
	return cmd
}

// configEnv is what every `config` subcommand needs: where to write its result, and
// the two persistent flags of the root command.
type configEnv struct {
	stdout     io.Writer
	stderr     io.Writer
	output     *string
	configPath *string
}

// format resolves --output. An invalid value is a usage error (exit 2).
func (e configEnv) format() (config.OutputFormat, error) {
	return config.ParseOutput(*e.output)
}

// noteShadowedOutput explains the one name that means two things.
//
// `config set` registers the real flags of the command it configures, and sort's
// --output has the same name as the root's persistent one. A local flag shadows an
// inherited one, so on `config set` --output names the setting to write, and this
// command's own report is left at the root's format.
//
// Neither half of that can be allowed to happen quietly: a user asking for JSON would
// otherwise have written a setting they did not mean to, or a user meaning to write
// the setting would have rendered JSON and saved nothing. The note fires only when
// --output was actually typed here, and it goes to stderr, where explanation belongs.
func noteShadowedOutput(stderr io.Writer, typed bool) {
	if !typed {
		return
	}
	_, _ = fmt.Fprintln(stderr,
		"note: --output here wrote the output setting; this command reports its own "+
			"result in the root format (use `moraine config show --output=json` for JSON)")
}

// newConfigShowCmd builds `config show`: the effective configuration, which is the
// file's value for a setting where it has one and the built-in default everywhere
// else. It is the answer to "what will this run actually use?", which neither the
// file nor --help can give on its own.
func newConfigShowCmd(env configEnv) *cobra.Command {
	origins := true
	cmd := &cobra.Command{
		Use:   "show [section]",
		Short: "Print the effective settings and where each came from",
		Long: `Print every setting a run would use: the configuration file's value where the file
sets one, and the built-in default everywhere else. Naming a section limits the
listing to it.

Each setting reports its origin — "file" or "default" — so a value that is not doing
what you expected can be traced without opening anything. There is no third origin:
environment variables select which file is read, never what a setting is.

Output:
  --output=text (the default) prints one "key=value origin=..." line per setting;
  --origins=false drops the origin for a bare key=value listing. --output=json prints
  one object, with each setting's value, origin and default.

Exit codes:
  0  success
  1  runtime failure (stdout could not be written)
  2  usage error (unknown section, invalid --output, or a configuration file that
     does not parse)`,
		Example: `  moraine config show
  moraine config show sort
  moraine config show --origins=false > my-settings.txt
  moraine config show --output=json | jq '.settings[] | select(.origin == "file")'`,
		Args:      cobra.MatchAll(cobra.MaximumNArgs(1), cobra.OnlyValidArgs),
		ValidArgs: sections,
		RunE: func(_ *cobra.Command, args []string) error {
			format, err := env.format()
			if err != nil {
				return err
			}
			file, loc, exists, err := readTarget(*env.configPath)
			if err != nil {
				return err // an unreadable or invalid file → usage (exit 2)
			}
			rep := configReport{
				Command:  "config",
				Path:     loc.Path,
				Exists:   exists,
				Settings: effective(file, sectionsToReport(args)),
			}
			return asRuntime(rep.emit(format, origins, env.stdout))
		},
	}
	cmd.Flags().BoolVar(&origins, "origins", true,
		"report where each setting came from; --origins=false prints bare key=value lines")
	return cmd
}

// newConfigPathCmd builds `config path`: which file is in effect, whether it is there,
// and why that file rather than another.
func newConfigPathCmd(env configEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print which configuration file is in effect",
		Long: `Print the configuration file this invocation resolves to, whether it exists, and
which rule chose it: --config, MORAINE_CONFIG, XDG_CONFIG_HOME, or the home
directory. It is the first thing to check when a setting appears to have done nothing.

The file named is also the one "config set" would write. Having no configuration file
is normal, and "exists=false" is not a failure.

Exit codes:
  0  success
  1  runtime failure (no file can be resolved at all — MORAINE_CONFIG is set to the
     empty string, or there is no home directory)
  2  usage error (invalid --output)`,
		Example: `  moraine config path
  moraine config path --output=json`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			format, err := env.format()
			if err != nil {
				return err
			}
			loc, err := configfile.Target(*env.configPath)
			if err != nil {
				return asRuntime(err)
			}
			_, statErr := os.Stat(loc.Path)
			rep := pathReport{
				Command: "config",
				Path:    loc.Path,
				Exists:  statErr == nil,
				Source:  loc.Source,
			}
			return asRuntime(rep.emit(format, env.stdout))
		},
	}
}

// readTarget reads the file the config tree acts on: the one `config set` would
// write, decoded, plus whether it is there at all.
//
// It is deliberately more forgiving than the Load a run uses. `config show` before
// the first `config set` must report all-defaults rather than fail, and `config edit`
// must be able to fill in a file that does not exist yet — which is also what makes
// MORAINE_CONFIG pointing at a new path a usable way to start one.
func readTarget(explicit string) (*configfile.File, configfile.Location, bool, error) {
	loc, err := configfile.Target(explicit)
	if err != nil {
		return nil, loc, false, asRuntime(err)
	}
	_, statErr := os.Stat(loc.Path)
	file, err := configfile.Read(loc.Path)
	if err != nil {
		return nil, loc, false, err
	}
	return file, loc, statErr == nil, nil
}

// sectionsToReport turns an optional section argument into the list to report.
func sectionsToReport(args []string) []string {
	if len(args) == 1 {
		return args[:1]
	}
	return sections
}

// effective lists the settings of the named sections as a run would see them: the
// file's value where the file sets one, the registered default otherwise.
func effective(file *configfile.File, list []string) []configSetting {
	out := make([]configSetting, 0, len(list)*len(sortOnlySettings))
	for _, section := range list {
		for _, s := range settingsFor(section) {
			def, _ := describe(section, s)
			value, fromFile := fileValue(file, section, s.YAML)
			origin := originFile
			if !fromFile {
				value, origin = def, originDefault
			}
			out = append(out, configSetting{
				Key: s.key(section), Value: value, Origin: origin, Default: def,
			})
		}
	}
	return out
}

// writeSettings is the one path that changes the configuration file. edit makes the
// change; everything around it is the same for `set`, `unset` and `edit`.
//
// The candidate is read back and run through the real config constructors before it
// is saved, so a file moraine writes is always one moraine can use: a value no run
// would accept is refused with the file untouched, rather than breaking the next run.
func writeSettings(env configEnv, opts writeOptions, edit func(*configfile.Document) error) error {
	format, err := env.format()
	if err != nil {
		return err
	}
	noteShadowedOutput(env.stderr, opts.TypedOutput)
	loc, err := configfile.Target(*env.configPath)
	if err != nil {
		return asRuntime(err)
	}
	doc, err := configfile.Open(loc.Path)
	if err != nil {
		return err // the file is not a set of settings → usage (exit 2)
	}
	if err := edit(doc); err != nil {
		return err
	}

	data, err := doc.Bytes()
	if err != nil {
		return asRuntime(err)
	}
	candidate, err := configfile.Parse(data)
	if err != nil {
		return fmt.Errorf("the resulting configuration file would not be readable: %w", err)
	}
	if err := checkSections(candidate); err != nil {
		return fmt.Errorf("%w (nothing was written to %s)", err, loc.Path)
	}

	changed, err := doc.Changed()
	if err != nil {
		return asRuntime(err)
	}
	// Nothing to do is not a write: a command that changes nothing must not create a
	// configuration file, and must not touch one that is already right.
	written := changed && !opts.DryRun
	if written {
		if err := doc.Save(); err != nil {
			return asRuntime(err)
		}
	}
	rep := configReport{
		Command:  "config",
		Path:     loc.Path,
		Exists:   doc.Existed() || written,
		Written:  written,
		DryRun:   opts.DryRun,
		Settings: effective(candidate, opts.Report),
	}
	return asRuntime(rep.emit(format, true, env.stdout))
}

// writeOptions is how a writing command varies: which sections it reports having
// changed, whether it actually commits, and whether the user typed the one flag whose
// name means something different here (see noteShadowedOutput).
type writeOptions struct {
	Report      []string
	DryRun      bool
	TypedOutput bool
}

// checkSections reads a candidate file back the way each command reads it — through
// the same overlay the real commands use, then the same constructors — so the check
// is the run's own, not a second opinion about it.
//
// The commands built here have parsed no flags, so nothing shadows the file: every
// value in it is exercised.
func checkSections(f *configfile.File) error {
	var output, progress, configPath string

	sortOpts := defaultSortOptions()
	sortOpts.Source = "."
	applySortFile(newSortCmd(io.Discard, io.Discard, &output, &progress, &configPath), &sortOpts, f)
	if _, err := config.New(sortOpts); err != nil {
		return err
	}

	cleanOpts := defaultCleanOptions()
	cleanOpts.Source = "."
	applyCleanFile(newCleanCmd(io.Discard, io.Discard, &output, &progress, &configPath), &cleanOpts, f)
	if _, err := config.NewClean(cleanOpts); err != nil {
		return err
	}

	undoOpts := defaultUndoOptions()
	undoOpts.Dest = "."
	applyUndoFile(newUndoCmd(io.Discard, io.Discard, &output, &progress, &configPath), &undoOpts, f)
	if _, err := config.NewUndo(undoOpts); err != nil {
		return err
	}
	return nil
}

// checkValues runs one section's settings, spelled as flag values, through the
// constructor that section's command uses. It is what the interactive form validates
// a field with, so the form refuses exactly what a run would refuse — including the
// cross-field rules, such as a theme that collides with the fallback theme.
//
// values is keyed by flag name. The shared section is checked with sort's
// constructor, which accepts all three of its settings.
func checkValues(section string, values map[string]string) error {
	f := pflag.NewFlagSet(section, pflag.ContinueOnError)
	switch section {
	case sectionClean:
		opts := config.CleanOptions{}
		registerCleanFlags(f, &opts)
		if err := setAll(f, values); err != nil {
			return err
		}
		opts.Source, opts.Output, opts.Progress = ".", values["output"], values["progress"]
		_, err := config.NewClean(opts)
		return err
	case sectionUndo:
		opts := config.UndoOptions{}
		registerUndoFlags(f, &opts)
		if err := setAll(f, values); err != nil {
			return err
		}
		opts.Dest, opts.Output, opts.Progress = ".", values["output"], values["progress"]
		_, err := config.NewUndo(opts)
		return err
	default:
		opts := config.Options{}
		registerSortFlags(f, &opts)
		if err := setAll(f, values); err != nil {
			return err
		}
		opts.Source, opts.Output, opts.Progress = ".", values["output"], values["progress"]
		_, err := config.New(opts)
		return err
	}
}

// setAll parses each value with the flag that owns it, which is where a malformed
// duration or number is reported — in the words pflag would use on the command line.
// A name the flag set does not have (--output and --progress, which live on the root
// command) is handled by the caller.
func setAll(f *pflag.FlagSet, values map[string]string) error {
	for _, name := range slices.Sorted(maps.Keys(values)) {
		if f.Lookup(name) == nil {
			continue
		}
		if err := f.Set(name, values[name]); err != nil {
			return err
		}
	}
	return nil
}
