package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/sgaunet/moraine/internal/configfile"
	"github.com/sgaunet/moraine/internal/configform"
)

// newConfigEditCmd builds `config edit`: the same settings as `config set`, chosen
// from a list and then filled in, rather than typed as flags.
//
// It asks in two steps — which settings, then what to set them to — because a form
// that asked about all of them would be two dozen questions deep, and huh only offers
// to submit on the last one. Picking first means the form is as long as the change,
// and never longer.
func newConfigEditCmd(env configEnv) *cobra.Command {
	var dryRun, accessible bool
	cmd := &cobra.Command{
		Use:   "edit [section]",
		Short: "Pick settings from a list and fill them in",
		Long: `Change settings interactively, in two steps: pick the ones you want to change from
a list, then answer a question for each. Naming a section limits the list to it; with
no argument the list covers every section.

The list shows each setting's current value and marks the ones your file already sets,
so it doubles as a way to find the setting you are after. Space picks, "/" filters,
enter moves on — and picking nothing is how you back out having changed nothing.

Each question then starts from the value in effect: the file's, or the built-in default
where the file says nothing. That value is there to be edited; ctrl+u clears it if you
would rather type a fresh one. Only answers you actually change are written, so a setting
you look at and accept is not written; answering a question with the default REMOVES the
setting, which is what "config unset" does.

The form draws on stderr, never stdout: stdout carries the resulting settings, so
"moraine config edit --output=json > settings.json" works while the form is on screen.

  --accessible replaces the full-screen form with plain question-and-answer prompts.
  It is what to use with a screen reader, and the only mode that works when standard
  input is not a terminal.

Exit codes:
  0  success (including picking nothing, which writes nothing)
  1  runtime failure (no terminal to draw on, an aborted form, or the file could not
     be saved)
  2  usage error (unknown section, or an answer no run could use)`,
		Example: `  moraine config edit
  moraine config edit sort
  moraine config edit sort --dry-run
  moraine config edit --accessible < answers.txt`,
		Args:      cobra.MatchAll(cobra.MaximumNArgs(1), cobra.OnlyValidArgs),
		ValidArgs: sections,
		RunE: func(cmd *cobra.Command, args []string) error {
			list := sectionsToReport(args)

			file, _, _, err := readTarget(*env.configPath)
			if err != nil {
				return err // an unreadable or invalid file → usage (exit 2)
			}

			in := cmd.InOrStdin()
			if !accessible && !isTerminal(in) {
				return asRuntime(errors.New(
					"no terminal to draw the form on; use --accessible to answer plain prompts, " +
						"or `moraine config set` to write settings as flags"))
			}
			screen := configform.Terminal{In: in, Out: env.stderr, Accessible: accessible}

			picked, err := pickSettings(cmd.Context(), screen, file, list)
			if err != nil {
				return formError(err)
			}
			if len(picked) == 0 {
				// Nothing to change is a legitimate answer, not a failure: report what
				// the settings are and leave the file alone.
				return reportOnly(env, file, list)
			}

			asked := valueQuestions(file, picked)
			answered, err := configform.Run(cmd.Context(), screen, asked)
			if err != nil {
				return formError(err)
			}

			return writeSettings(env,
				writeOptions{Report: list, DryRun: dryRun},
				func(doc *configfile.Document) error {
					return applyAnswers(doc, picked, asked, answered)
				})
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&dryRun, "dry-run", "n", false,
		"report the settings that would result, without writing the file")
	f.BoolVar(&accessible, "accessible", false,
		"ask plain question-and-answer prompts instead of drawing a form "+
			"(for a screen reader, or when standard input is not a terminal)")
	return cmd
}

// choice is one setting the user picked, kept with the section it belongs to since the
// same setting name means a different thing in each.
type choice struct {
	Section string
	Setting setting
}

// pickSettings asks which settings to change. It is one question, so submitting it is
// always a single keystroke away — the property the two-step shape exists to give.
func pickSettings(
	ctx context.Context,
	screen configform.Terminal,
	file *configfile.File,
	list []string,
) ([]choice, error) {
	all := make([]choice, 0, len(list)*len(sortOnlySettings))
	for _, section := range list {
		for _, s := range settingsFor(section) {
			all = append(all, choice{Section: section, Setting: s})
		}
	}

	width := 0
	for _, c := range all {
		width = max(width, len(c.Setting.key(c.Section)))
	}
	options := make([]configform.Option, 0, len(all))
	for _, c := range all {
		options = append(options, configform.Option{
			Label: pickLabel(file, c, width),
			Value: c.Setting.key(c.Section),
		})
	}

	answered, err := configform.Run(ctx, screen, []configform.Group{{
		Title:       "Which settings do you want to change?",
		Description: "space picks, enter moves on — picking nothing changes nothing",
		Fields: []configform.Field{{
			Title:   "settings",
			Help:    "the value shown is the one in effect; ← marks the ones your file sets",
			Kind:    configform.Multi,
			Options: options,
		}},
	}})
	if err != nil {
		return nil, err
	}

	wanted := make(map[string]bool, len(answered[0].Fields[0].Values))
	for _, key := range answered[0].Fields[0].Values {
		wanted[key] = true
	}
	// Kept in the listing's order rather than the order they were picked, so the
	// questions arrive in the order the list showed them.
	out := make([]choice, 0, len(wanted))
	for _, c := range all {
		if wanted[c.Setting.key(c.Section)] {
			out = append(out, c)
		}
	}
	return out, nil
}

// pickLabel renders one line of the list: the setting, the value in effect, and a mark
// for the ones the file sets, so the list also answers "what did I configure again?".
func pickLabel(file *configfile.File, c choice, width int) string {
	def, _ := describe(c.Section, c.Setting)
	value, fromFile := fileValue(file, c.Section, c.Setting.YAML)
	if !fromFile {
		value = def
	}
	if value == "" {
		value = "(unset)"
	}
	label := fmt.Sprintf("%-*s  %s", width, c.Setting.key(c.Section), value)
	if fromFile {
		label += "  ←"
	}
	return label
}

// valueQuestions asks for the value of each picked setting, starting from the one in
// effect. They share one page: the form is now as long as the change, so a second page
// would only add a keystroke.
func valueQuestions(file *configfile.File, picked []choice) []configform.Group {
	fields := make([]configform.Field, 0, len(picked))
	for _, c := range picked {
		def, help := describe(c.Section, c.Setting)
		value, fromFile := fileValue(file, c.Section, c.Setting.YAML)
		if !fromFile {
			value = def
		}
		fields = append(fields, configform.Field{
			Title:    c.Setting.key(c.Section),
			Help:     help + "\n(default: " + defaultLabel(def) + " — answering it removes the setting)",
			Kind:     formKind(c.Setting),
			Options:  configform.NewOptions(c.Setting.Choices...),
			Value:    value,
			Validate: validatorFor(c.Section, c.Setting, currentValues(file, c.Section)),
		})
	}
	return []configform.Group{{
		Title:       "New values",
		Description: "each starts from the value in effect — ctrl+u clears it; enter moves on, the last one saves",
		Fields:      fields,
	}}
}

// defaultLabel names a default for a prompt, spelling the empty one out.
func defaultLabel(def string) string {
	if def == "" {
		return "empty"
	}
	return def
}

// formKind decides how a setting is asked for: a closed set becomes a list, a
// boolean a yes/no question, everything else a typed answer.
func formKind(s setting) configform.Kind {
	switch {
	case len(s.Choices) > 0:
		return configform.Choice
	case s.Kind == kindBool:
		return configform.Toggle
	default:
		return configform.Text
	}
}

// currentValues collects a section's effective settings keyed by flag name, which is
// the shape checkValues wants.
func currentValues(file *configfile.File, section string) map[string]string {
	out := make(map[string]string, len(settingsFor(section)))
	for _, s := range settingsFor(section) {
		def, _ := describe(section, s)
		value, fromFile := fileValue(file, section, s.YAML)
		if !fromFile {
			value = def
		}
		out[s.Flag] = value
	}
	return out
}

// validatorFor builds the check a typed answer must pass: the answer, dropped into
// the section's other settings, run through the very constructor a run would use. So
// the form refuses a malformed duration, a confidence outside 0..1 and a theme that
// collides with the fallback theme, all without a second copy of any of those rules.
//
// A list or a yes/no question needs no validator: its answers are a closed set.
//
// The other settings are the ones in effect when the form opened, not as the user is
// editing them, so a pair changed together — swapping a theme and the fallback theme
// in one sitting — can still be reported at the end rather than inline. That check
// runs before anything is written, so such a pair is refused, never saved.
func validatorFor(section string, s setting, current map[string]string) func(string) error {
	if len(s.Choices) > 0 || s.Kind == kindBool {
		return nil
	}
	return func(answer string) error {
		candidate := make(map[string]string, len(current))
		for k, v := range current {
			candidate[k] = v
		}
		candidate[s.Flag] = answer
		return checkValues(section, candidate)
	}
}

// applyAnswers records only what the user actually changed.
//
// Leaving an untouched setting alone is not an optimisation: writing every answer
// would stamp today's defaults into the file for every question merely accepted, and
// a later change to a default would then never reach this user. It is also what keeps
// the comment on a line nobody edited exactly where it was.
func applyAnswers(doc *configfile.Document, picked []choice, asked, answered []configform.Group) error {
	for i, c := range picked {
		was, now := asked[0].Fields[i].Value, answered[0].Fields[i].Value
		if was == now {
			continue
		}
		def, _ := describe(c.Section, c.Setting)
		if now == def {
			// Back to the default: remove it rather than pin it, which is what
			// `config unset` does and what origin=default then reports.
			doc.Unset(c.Setting.path(c.Section))
			continue
		}
		if err := doc.Set(c.Setting.path(c.Section), valueNode(c.Setting, now)); err != nil {
			return err
		}
	}
	return nil
}

// reportOnly emits the settings without touching the file, for a form that ended with
// nothing to change.
func reportOnly(env configEnv, file *configfile.File, list []string) error {
	format, err := env.format()
	if err != nil {
		return err
	}
	loc, err := configfile.Target(*env.configPath)
	if err != nil {
		return asRuntime(err)
	}
	_, statErr := os.Stat(loc.Path)
	rep := configReport{
		Command:  "config",
		Path:     loc.Path,
		Exists:   statErr == nil,
		Settings: effective(file, list),
	}
	return asRuntime(rep.emit(format, true, env.stdout))
}

// formError turns a form's failure into the CLI's vocabulary. Leaving the form is not
// a crash — it is a decision, and the only thing that matters is that nothing was
// written.
func formError(err error) error {
	if errors.Is(err, configform.ErrAborted) {
		return asRuntime(errors.New("aborted: nothing was written"))
	}
	return asRuntime(err)
}

// isTerminal reports whether r is a terminal moraine may draw a form on. Anything
// that is not an os.File — a pipe, a test's reader — is not.
//
// This is Constitution Principle V's "no full-screen interface when there is no
// terminal". It asks the terminal driver rather than checking os.ModeCharDevice,
// which /dev/null also has: `moraine config edit < /dev/null` would otherwise open a
// form nobody can answer. The module is the one huh already uses to decide the same
// question, so it adds nothing to the dependency graph.
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(f.Fd())
}
