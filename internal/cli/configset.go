package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sgaunet/moraine/internal/configfile"
)

// newConfigSetCmd builds `config set`, which carries one subcommand per section of
// the file. A section per command is what lets each one register the *real* flags of
// the command it configures — the same pflag types, so a malformed duration is
// reported in the same words as `moraine sort --gap nope`, and the same shorthands,
// so a habit carries over.
func newConfigSetCmd(env configEnv) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <section> [flags]",
		Short: "Write settings to the configuration file",
		Long: `Write settings to the configuration file. Each section takes the flags of the
command it configures, so --gap here means what --gap means to sort.

Only the flags you actually type are written. A flag left out is left alone, and a
flag whose value happens to equal the default is still written — typing it means you
want it pinned, which is what makes a later change to the default not move your
library.

--output here names the SETTING to write, since it is one of the settings the commands
accept — it does not change how this command reports its own result, which stays in
the root format. Using it says so on stderr rather than leaving the difference to be
discovered. For a machine-readable view, "moraine config show --output=json".

Comments and everything else in the file are preserved. The result is checked before
it is saved: a value no run could use is refused with the file untouched.`,
		Example: `  moraine config set sort --gap 8h --jobs 4 --vote
  moraine config set shared --log-level warn --output json
  moraine config set clean --dest /Volumes/photos/sorted
  moraine config set sort --dry-run --themes "hiking,party,food"`,
	}
	for _, section := range sections {
		cmd.AddCommand(newConfigSetSectionCmd(env, section))
	}
	return cmd
}

// newConfigSetSectionCmd builds `config set <section>`.
func newConfigSetSectionCmd(env configEnv, section string) *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   section + " [flags]",
		Short: "Write " + sectionSummary(section),
		Long: fmt.Sprintf(`Write %s.

Every flag below is the one %s carries, and means the same thing. Only the flags you
type are written to the file.

Exit codes:
  0  success
  1  runtime failure (nowhere to write, or the file could not be saved)
  2  usage error (no setting given, an invalid value, or a value no run could use)`,
			sectionSummary(section), sectionOwner(section)),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			changed := changedSettings(cmd, section)
			if len(changed) == 0 {
				return fmt.Errorf(
					"no setting given: pass at least one of %s (see --help)",
					flagList(section))
			}
			return writeSettings(env,
				writeOptions{
					Report:      []string{section},
					DryRun:      dryRun,
					TypedOutput: cmd.Flags().Changed("output"),
				},
				func(doc *configfile.Document) error {
					for _, s := range changed {
						raw := cmd.Flags().Lookup(s.Flag).Value.String()
						if err := doc.Set(s.path(section), valueNode(s, raw)); err != nil {
							return err
						}
					}
					return nil
				})
		},
	}
	registerSettingFlags(cmd, section)
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false,
		"report the settings that would result, without writing the file")
	return cmd
}

// newConfigUnsetCmd builds `config unset <section> <setting>...`, which removes
// settings so they fall back to the built-in default. It is the way back from a
// setting written by mistake, without opening the file.
//
// It takes setting names rather than flags: a flag would need a value it has no use
// for, and "unset --gap 0" reads like it sets something.
func newConfigUnsetCmd(env configEnv) *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "unset <section> <setting>...",
		Short: "Remove settings, so they fall back to the default",
		Long: `Remove one or more settings from the configuration file. Each returns to its
built-in default, which "moraine config show" then reports with origin=default.

A setting may be named either way round — "path-template" or "path_template" — since
the flag and the key are the same word. Removing the last setting of a section removes
the section too, and a setting that was not there is reported rather than treated as a
failure. Everything else in the file, comments included, is left as it was.

Exit codes:
  0  success
  1  runtime failure (nowhere to write, or the file could not be saved)
  2  usage error (unknown section or setting)`,
		Example: `  moraine config unset sort gap
  moraine config unset sort gap jobs vote
  moraine config unset shared log_level`,
		Args:              cobra.MinimumNArgs(2),
		ValidArgsFunction: completeUnset,
		RunE: func(_ *cobra.Command, args []string) error {
			section := args[0]
			if !slices.Contains(sections, section) {
				return fmt.Errorf("unknown section %q: expected one of %s",
					section, strings.Join(sections, ", "))
			}
			wanted := make([]setting, 0, len(args)-1)
			for _, name := range args[1:] {
				s, ok := lookupSetting(section, name)
				if !ok {
					return fmt.Errorf("the %s section has no setting %q: expected one of %s",
						section, name, settingList(section))
				}
				wanted = append(wanted, s)
			}
			return writeSettings(env,
				writeOptions{Report: []string{section}, DryRun: dryRun},
				func(doc *configfile.Document) error {
					for _, s := range wanted {
						doc.Unset(s.path(section))
					}
					return nil
				})
		},
	}
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false,
		"report the settings that would result, without writing the file")
	return cmd
}

// registerSettingFlags declares one flag per settable value of a section.
//
// The flags carry no default of their own — an unset flag means "leave this alone",
// and a value is only ever read when Changed reports the user typed it — but the
// help text and the shorthand are taken from the real command, so the two cannot
// come to describe the same setting differently.
func registerSettingFlags(cmd *cobra.Command, section string) {
	f := cmd.Flags()
	ref := referenceFlags(section)
	for _, s := range settingsFor(section) {
		_, help := describe(section, s)
		short := ""
		if r := ref.Lookup(s.Flag); r != nil {
			short = r.Shorthand
		}
		switch s.Kind {
		case kindBool:
			f.BoolP(s.Flag, short, false, help)
		case kindInt:
			f.IntP(s.Flag, short, 0, help)
		case kindFloat:
			f.Float64P(s.Flag, short, 0, help)
		case kindDuration:
			f.DurationP(s.Flag, short, 0, help)
		case kindString, kindList:
			f.StringP(s.Flag, short, "", help)
		}
		if len(s.Choices) > 0 {
			_ = cmd.RegisterFlagCompletionFunc(s.Flag, completeFixed(s.Choices...))
		}
	}
	registerSettingCompletions(cmd, section)
}

// changedSettings returns the settings whose flag the user actually typed. Asking
// cobra rather than comparing against a default is what lets `config set sort
// --gap 6h` pin the default on purpose.
func changedSettings(cmd *cobra.Command, section string) []setting {
	out := make([]setting, 0, len(settingsFor(section)))
	for _, s := range settingsFor(section) {
		if cmd.Flags().Changed(s.Flag) {
			out = append(out, s)
		}
	}
	return out
}

// flagList names a section's settings as the flags `config set` takes.
func flagList(section string) string {
	return joinSettings(section, func(s setting) string { return "--" + s.Flag })
}

// settingList names them as the words `config unset` takes.
func settingList(section string) string {
	return joinSettings(section, func(s setting) string { return s.YAML })
}

// joinSettings renders a section's settings for an error message.
func joinSettings(section string, name func(setting) string) string {
	settings := settingsFor(section)
	names := make([]string, 0, len(settings))
	for _, s := range settings {
		names = append(names, name(s))
	}
	return strings.Join(names, ", ")
}

// sectionSummary describes what a section holds, for its --help line.
func sectionSummary(section string) string {
	switch section {
	case sectionShared:
		return "the settings every command inherits"
	case sectionUndo:
		return "undo's settings"
	default:
		return section + "'s settings"
	}
}

// sectionOwner names the command a section's flags belong to.
func sectionOwner(section string) string {
	if section == sectionShared {
		return "sort"
	}
	return section
}
