package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sgaunet/moraine/internal/config"
	"github.com/sgaunet/moraine/internal/configfile"
	"github.com/sgaunet/moraine/internal/configform"
	"github.com/sgaunet/moraine/internal/organize"
)

// newConfigWizardCmd builds `config wizard`: a short, fixed sequence of questions that
// covers the decisions a first configuration file is made of, in the order a new user
// would think about them.
//
// It is `config edit`'s machinery with a different list of questions. edit asks about
// settings, by name, and assumes the user knows which one they are after; the wizard
// asks about outcomes — where copies go, how folders are laid out, how events are
// recognised — and works out the settings from the answers. Each step is its own form,
// run one after the other, so which follow-up comes next is an ordinary if on the
// answers already given, and a question can be validated against them.
func newConfigWizardCmd(env configEnv) *cobra.Command {
	var dryRun, accessible bool
	cmd := &cobra.Command{
		Use:   "wizard",
		Short: "Create or revisit the configuration file by answering a few questions",
		Long: `Answer a short series of questions and get a configuration file that says what you
answered. It is the place to start when you have no file yet, or want to revisit the
decisions a file is made of:

  1. where sorted copies go
  2. which themes photos are sorted into (and any of your own)
  3. the theme for photos no other theme fits
  4. how folders are laid out below the destination
  5. whether companion files (IMG.jpg.xmp, …) travel with their photo
  6. how events are recognised: the local vision model, the vision model with a
     decision service (Laya, self-hosted, or Jev, hosted), or no model at all —
     and, depending on the answer, the model and service addresses
  7. how much runs log, and how they show progress

Use "config edit" instead when you know which setting you want to change — it lists
every one, including the tuning settings the wizard leaves alone (gap, sample,
confidence thresholds, vote, jobs, altitude, exiftool, theme descriptions). Use
"config set" in a script.

Every question starts from the value in effect: your file's, or the built-in default
where the file says nothing, and the default is named in each question. Only answers
you change are written, and answering the default REMOVES the setting, so the file
never pins today's defaults. Comments and settings the wizard does not ask about are
kept. If a command's own section overrides one of your answers (say, a dest under
sort:), the wizard names it and asks before removing it.

The wizard only goes forward: the fields of one step can be revisited, a step already
answered cannot. To redo one, answer no at the summary, which writes nothing, and
run it again. Nothing is saved until you confirm the summary.

API keys are never asked for and never written. sort reads them from the
environment: LAYA_API_KEY (Laya, optional) or TYPESAFE_API_KEY (Jev, required). The
wizard contacts nothing, so a wrong address or model name shows up on the first sort.

The form draws on stderr; stdout carries the resulting settings (--output=text|json).
--accessible replaces the full-screen form with plain question-and-answer prompts, for
a screen reader or when standard input is not a terminal. --dry-run asks everything
and shows what would be written, without writing it.

Exit codes:
  0  success (including nothing to change, and answering no at the summary)
  1  runtime failure (no terminal to draw on, an aborted form, no configuration file
     can be resolved, or the file could not be saved)
  2  usage error (an argument, an unknown flag, or a configuration file that does not
     parse)`,
		Example: `  moraine config wizard
  moraine config wizard --dry-run
  MORAINE_CONFIG=./moraine.yaml moraine config wizard
  moraine config wizard --accessible`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			file, loc, _, err := readTarget(*env.configPath)
			if err != nil {
				return err // no file can be resolved → runtime (1); an invalid one → usage (2)
			}

			in := cmd.InOrStdin()
			if !accessible && !isTerminal(in) {
				return asRuntime(errors.New(
					"no terminal to draw the wizard on; use --accessible to answer plain prompts, " +
						"or `moraine config set` to write settings as flags"))
			}

			w := newWizard(configform.Terminal{In: in, Out: env.stderr, Accessible: accessible}, file)
			if err := w.run(cmd.Context()); err != nil {
				return formError(err)
			}
			changes, err := w.decide(cmd.Context(), loc.Path, dryRun)
			if err != nil {
				return formError(err)
			}
			if changes == nil {
				return reportOnly(env, file, sections)
			}
			if err := writeSettings(env,
				writeOptions{Report: sections, DryRun: dryRun},
				func(doc *configfile.Document) error { return apply(doc, changes) }); err != nil {
				return err
			}
			if w.usesDecider() {
				w.keyHint() // said again at the end, where it is read after the form has gone
			}
			return nil
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

// wizardSetting is one setting the wizard can write, with the section it is written
// to. Shared settings go to the top level, so sort, clean and undo all inherit them;
// the rest are sort's own.
type wizardSetting struct {
	Section string
	Flag    string
}

// wizardSettings lists every setting a wizard session may change, in the order the
// summary reports them. The answers are keyed by flag name, which is the shape
// checkValues and valueNode already take.
var wizardSettings = []wizardSetting{
	{sectionShared, "dest"},
	{sectionSort, "themes"},
	{sectionSort, "fallback-theme"},
	{sectionSort, "path-template"},
	{sectionSort, "sidecars"},
	{sectionSort, "sample"},
	{sectionSort, "model"},
	{sectionSort, "ollama-url"},
	{sectionSort, "decider"},
	{sectionSort, "decider-url"},
	{sectionSort, "decider-model"},
	{sectionShared, "log-level"},
	{sectionShared, "progress"},
}

// setting returns the table entry a wizard setting names. Every name above is in the
// table, which the tests exercise, so a miss is a programming error.
func (s wizardSetting) setting() setting {
	found, _ := lookupSetting(s.Section, s.Flag)
	return found
}

// defaultValue is the value the setting takes when the file says nothing, as --help
// states it.
func (s wizardSetting) defaultValue() string {
	def, _ := describe(s.Section, s.setting())
	return def
}

// wizard holds a session: the file it started from, the answers as they stood before
// the first question, and the answers as they are now.
//
// Three answers are not settings but choices the settings are worked out from: the
// themes picked from the list (and whether the user wants to add their own), the
// folder layout, and the classification strategy.
type wizard struct {
	screen  configform.Terminal
	stderr  io.Writer
	file    *configfile.File
	start   map[string]string
	answers map[string]string

	picked   []string
	custom   bool
	layout   string
	strategy string
}

func newWizard(screen configform.Terminal, file *configfile.File) *wizard {
	start := startingAnswers(file)
	return &wizard{
		screen:  screen,
		stderr:  screen.Out,
		file:    file,
		start:   start,
		answers: maps.Clone(start),
	}
}

// startingAnswers is the value every question starts from: the file's where it sets
// one, the default where it does not. A shared setting is read from the top level —
// the place the wizard writes it — rather than through a command's section, so an
// override inside a section is reported as one, not mistaken for the shared value.
func startingAnswers(file *configfile.File) map[string]string {
	out := make(map[string]string, len(wizardSettings))
	for _, ws := range wizardSettings {
		value, fromFile := fileValue(file, ws.Section, ws.setting().YAML)
		if !fromFile {
			value = ws.defaultValue()
		}
		out[ws.Flag] = value
	}
	return out
}

// step is one page of the wizard: one form, asked only when ask says so (a nil ask
// means always), built from the answers given so far, and folded back into them.
type step struct {
	ask   func() bool
	group func() configform.Group
	take  func(answered configform.Group)
}

// steps lists the pages in the order they are asked (spec FR-002).
func (w *wizard) steps() []step {
	return []step{
		w.destStep(),
		w.themesStep(),
		w.customThemesStep(),
		w.fallbackStep(),
		w.layoutStep(),
		w.templateStep(),
		w.sidecarsStep(),
		w.classificationStep(),
		w.visionModelStep(),
		w.deciderStep(),
		w.outputStep(),
	}
}

// titled puts a question's default in its title. huh's accessible prompts print the
// title and nothing else, so a default named only in the help would be invisible to
// the users who most need to be told it.
func titled(question, def string) string {
	return question + " (default: " + def + ")"
}

// markDefault marks the option of a list that is the default.
func markDefault(options []configform.Option, def string) []configform.Option {
	for i := range options {
		if options[i].Value == def {
			options[i].Label += " (default)"
		}
	}
	return options
}

// single wraps one field as a page.
func single(f configform.Field) configform.Group {
	return configform.Group{Fields: []configform.Field{f}}
}

// check validates one answer through the constructor sort uses, with the answers it
// depends on beside it.
func check(values map[string]string) error {
	return checkValues(sectionSort, values)
}

// destStep asks where sorted copies go.
func (w *wizard) destStep() step {
	return step{
		group: func() configform.Group {
			return single(configform.Field{
				Title:    titled("Where should sorted copies go?", "<source>/_sorted"),
				Help:     "an absolute path, or ~/…; empty keeps the default next to each source",
				Value:    w.answers["dest"],
				Validate: func(a string) error { _, err := expandDest(a); return err },
			})
		},
		take: func(g configform.Group) {
			w.answers["dest"], _ = expandDest(g.Fields[0].Value)
		},
	}
}

// expandDest turns a typed destination into the one to write.
//
// Nothing expands text typed into a form the way a shell expands a command line, and
// nothing in moraine expands "~" when it reads the file: written verbatim, "~/Photos"
// would make every run create a directory named "~" wherever it was started. A
// relative path would move with the run's working directory in the same way, so it is
// refused rather than guessed at. Empty is the default, <source>/_sorted.
func expandDest(typed string) (string, error) {
	dest := strings.TrimSpace(typed)
	if dest == "" {
		return "", nil
	}
	if dest == "~" || strings.HasPrefix(dest, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot expand ~: %w", err)
		}
		dest = filepath.Join(home, strings.TrimPrefix(dest, "~"))
	}
	if !filepath.IsAbs(dest) {
		return "", errors.New("use an absolute path (or ~/…): a relative one would depend on where sort is run")
	}
	return filepath.Clean(dest), nil
}

// customThemesOption is the list entry that asks to add themes of one's own. It can
// never be a theme itself: a theme is a [a-z0-9-] slug.
const customThemesOption = "+custom"

// themesStep asks which themes to sort into: the built-in ones and any the file
// already adds, with the ones in effect ticked.
func (w *wizard) themesStep() step {
	return step{
		group: func() configform.Group {
			current := splitThemes(w.answers["themes"])
			options := configform.NewOptions(mergeThemes(splitThemes(config.DefaultThemes), current)...)
			options = append(options, configform.Option{Label: "+ add my own themes…", Value: customThemesOption})
			return single(configform.Field{
				Title:   "Which themes should photos be sorted into?",
				Help:    "space ticks, enter moves on; the last entry adds themes of your own",
				Kind:    configform.Multi,
				Options: options,
				Values:  current,
				ValidateMulti: func(picked []string) error {
					if len(picked) == 0 {
						return errors.New("pick at least one theme, or add your own")
					}
					return nil
				},
			})
		},
		take: func(g configform.Group) {
			values := g.Fields[0].Values
			w.custom = slices.Contains(values, customThemesOption)
			w.picked = slices.DeleteFunc(slices.Clone(values), func(v string) bool { return v == customThemesOption })
			w.answers["themes"] = strings.Join(w.picked, ",")
		},
	}
}

// customThemesStep asks for themes of the user's own, when they asked to add some.
// The themes the file already adds are in the list above, so this starts empty.
func (w *wizard) customThemesStep() step {
	merged := func(typed string) []string { return mergeThemes(w.picked, splitThemes(typed)) }
	return step{
		ask: func() bool { return w.custom },
		group: func() configform.Group {
			return single(configform.Field{
				Title: "Your own themes, comma-separated",
				Help:  "lower-case letters, digits and dashes, e.g. boats, garden",
				Validate: func(a string) error {
					themes := merged(a)
					if len(themes) == 0 {
						return errors.New("add at least one theme, or pick one from the list")
					}
					return checkThemes(themes)
				},
			})
		},
		take: func(g configform.Group) {
			w.answers["themes"] = strings.Join(merged(g.Fields[0].Value), ",")
		},
	}
}

// checkThemes validates a theme list on its own. config.New also refuses a theme that
// is the fallback theme, but the fallback is the next question: it is checked there,
// against these themes, so here it stands in with a name the list cannot hold.
func checkThemes(themes []string) error {
	fallback := "fallback"
	for slices.Contains(themes, fallback) {
		fallback += "-x"
	}
	return check(map[string]string{"themes": strings.Join(themes, ","), "fallback-theme": fallback})
}

// splitThemes reads a comma-separated theme list.
func splitThemes(list string) []string {
	var out []string
	for raw := range strings.SplitSeq(list, ",") {
		if theme := strings.TrimSpace(raw); theme != "" {
			out = append(out, theme)
		}
	}
	return out
}

// mergeThemes appends the themes of more that are not in base yet, in order.
func mergeThemes(base, more []string) []string {
	out := slices.Clone(base)
	for _, theme := range more {
		if !slices.Contains(out, theme) {
			out = append(out, theme)
		}
	}
	return out
}

// fallbackStep asks for the theme of photos no other theme fits, checked against the
// themes just chosen rather than the ones the session started with.
func (w *wizard) fallbackStep() step {
	return step{
		group: func() configform.Group {
			return single(configform.Field{
				Title: titled("Theme for photos no other theme fits", config.DefaultFallback),
				Value: w.answers["fallback-theme"],
				Validate: func(a string) error {
					return check(map[string]string{"themes": w.answers["themes"], "fallback-theme": a})
				},
			})
		},
		take: func(g configform.Group) {
			w.answers["fallback-theme"] = strings.TrimSpace(g.Fields[0].Value)
		},
	}
}

// layoutPresets are the folder layouts offered by name. The first is the default.
var layoutPresets = []struct {
	Label    string
	Template string
}{
	{"by theme, then year, then day", config.DefaultPathTemplate},
	{"by year, then month, then theme", "{year}/{month}/{theme}"},
	{"by theme, then day", "{theme}/{date}"},
}

// layoutCustom is the list entry that asks for a template of one's own.
const layoutCustom = "custom"

// layoutExample renders a template for one made-up event, so each option shows the
// path it produces — rendered by the code a run uses, so the example cannot drift.
func layoutExample(template string) string {
	t, err := organize.ParseTemplate(template)
	if err != nil {
		return ""
	}
	return t.Render("mountain", time.Date(2025, time.July, 14, 9, 0, 0, 0, time.UTC))
}

// layoutStep asks how folders are laid out below the destination. The example path
// is part of each option's label, which accessible mode prints.
func (w *wizard) layoutStep() step {
	return step{
		group: func() configform.Group {
			options := make([]configform.Option, 0, len(layoutPresets)+1)
			current := layoutCustom
			for _, p := range layoutPresets {
				options = append(options, configform.Option{
					Label: p.Label + " — e.g. " + layoutExample(p.Template),
					Value: p.Template,
				})
				if p.Template == w.answers["path-template"] {
					current = p.Template
				}
			}
			options = markDefault(options, config.DefaultPathTemplate)
			options = append(options, configform.Option{Label: "a template of my own…", Value: layoutCustom})
			return single(configform.Field{
				Title:   "How should folders be laid out?",
				Kind:    configform.Choice,
				Options: options,
				Value:   current,
			})
		},
		take: func(g configform.Group) {
			w.layout = g.Fields[0].Value
			if w.layout != layoutCustom {
				w.answers["path-template"] = w.layout
			}
		},
	}
}

// templateStep asks for a layout template of the user's own.
func (w *wizard) templateStep() step {
	return step{
		ask: func() bool { return w.layout == layoutCustom },
		group: func() configform.Group {
			return single(configform.Field{
				Title: titled("Folder template, from {theme} {year} {month} {day} {date}", config.DefaultPathTemplate),
				Value: w.answers["path-template"],
				Validate: func(a string) error {
					return check(map[string]string{"path-template": a})
				},
			})
		},
		take: func(g configform.Group) {
			w.answers["path-template"] = strings.TrimSpace(g.Fields[0].Value)
		},
	}
}

// sidecarsStep asks whether companion files travel with their photo.
func (w *wizard) sidecarsStep() step {
	return step{
		group: func() configform.Group {
			return single(configform.Field{
				Title: titled("Copy companion files (IMG.jpg.xmp, IMG.xmp, …) next to each photo?", "yes"),
				Kind:  configform.Toggle,
				Value: w.answers["sidecars"],
			})
		},
		take: func(g configform.Group) {
			w.answers["sidecars"] = g.Fields[0].Value
		},
	}
}

// The classification strategies. Each is a combination of settings rather than a
// setting: "no model" is sample 0, and a decision service is a decider.
const (
	strategyVision = "vision"
	strategyLaya   = string(config.DeciderLaya)
	strategyJev    = string(config.DeciderJev)
	strategyNone   = "none"
)

// currentStrategy reads the strategy the answers amount to.
func (w *wizard) currentStrategy() string {
	switch {
	case w.answers["sample"] == "0":
		return strategyNone
	case w.answers["decider"] == strategyLaya, w.answers["decider"] == strategyJev:
		return w.answers["decider"]
	default:
		return strategyVision
	}
}

// usesDecider reports whether the strategy is one of the decision services.
func (w *wizard) usesDecider() bool {
	return w.strategy == strategyLaya || w.strategy == strategyJev
}

// classificationStep asks how events are recognised, as one list rather than a
// strategy followed by a service: one question fewer, and every option says what it
// costs. Jev's label carries the disclosure `sort --help` makes about what it sends.
func (w *wizard) classificationStep() step {
	return step{
		group: func() configform.Group {
			return single(configform.Field{
				Title: "How should events be recognised?",
				Kind:  configform.Choice,
				Options: []configform.Option{
					{Label: "the local vision model picks a theme (default)", Value: strategyVision},
					{Label: "the vision model describes, Laya decides (self-hosted)", Value: strategyLaya},
					{Label: "the vision model describes, Jev decides (hosted: the descriptions, " +
						"never the photos, go to api.typesafe.ai)", Value: strategyJev},
					{Label: "no model: the altitude rule, then the fallback theme", Value: strategyNone},
				},
				Value: w.currentStrategy(),
			})
		},
		take: func(g configform.Group) {
			w.takeStrategy(g.Fields[0].Value)
		},
	}
}

// takeStrategy works the strategy out into settings.
//
// Leaving "no model" restores the default sample rather than keeping 0; any other
// sample the file set is the user's own tuning and is kept. A decision service's
// address and model belong to that service, so they are cleared when it is no longer
// used or is swapped for the other one — an address left over from Laya would only be
// wrong for Jev.
func (w *wizard) takeStrategy(strategy string) {
	w.strategy = strategy
	sample := w.start["sample"]
	if sample == "0" {
		sample = wizardSetting{sectionSort, "sample"}.defaultValue()
	}
	decider := string(config.DeciderOff)
	switch strategy {
	case strategyNone:
		sample = "0"
	case strategyLaya, strategyJev:
		decider = strategy
	}
	w.answers["sample"], w.answers["decider"] = sample, decider
	if decider != w.start["decider"] {
		w.answers["decider-url"], w.answers["decider-model"] = "", ""
	}
	if w.usesDecider() {
		w.keyHint()
	}
}

// keyHint says where the decision service's key comes from. It names the variable
// and never reads it: a key is never asked for, shown or written (Principle IX).
func (w *wizard) keyHint() {
	d := config.Decider(w.strategy)
	need := "optional"
	if d == config.DeciderJev {
		need = "required"
	}
	_, _ = fmt.Fprintf(w.stderr,
		"note: sort reads the %s API key from %s (%s); it is never asked for or written to the file\n",
		d, d.APIKeyEnv(), need)
}

// visionModelStep asks which vision model to use and where Ollama answers.
func (w *wizard) visionModelStep() step {
	return step{
		ask: func() bool { return w.strategy != strategyNone },
		group: func() configform.Group {
			return configform.Group{Fields: []configform.Field{
				{
					Title:    titled("Vision model", config.DefaultModel),
					Value:    w.answers["model"],
					Validate: func(a string) error { return check(map[string]string{"model": a}) },
				},
				{
					Title:    titled("Ollama address", config.DefaultOllamaURL),
					Value:    w.answers["ollama-url"],
					Validate: func(a string) error { return check(map[string]string{"ollama-url": a}) },
				},
			}}
		},
		take: func(g configform.Group) {
			w.answers["model"] = strings.TrimSpace(g.Fields[0].Value)
			w.answers["ollama-url"] = strings.TrimSpace(g.Fields[1].Value)
		},
	}
}

// deciderDefaults returns the address and model a decision service uses when the file
// names neither. The flags' own default is empty, meaning exactly these.
func deciderDefaults(strategy string) (url, model string) {
	if strategy == strategyJev {
		return config.DefaultJevURL, config.DefaultJevModel
	}
	return config.DefaultLayaURL, config.DefaultLayaModel
}

// deciderStep asks for the decision service's address and model. Each starts from the
// service's own default, shown as a real value; answering it writes nothing, since
// "empty" already means that default.
func (w *wizard) deciderStep() step {
	orDefault := func(value, def string) string {
		if value == "" {
			return def
		}
		return value
	}
	orEmpty := func(value, def string) string {
		if value = strings.TrimSpace(value); value == def {
			return ""
		}
		return value
	}
	return step{
		ask: w.usesDecider,
		group: func() configform.Group {
			url, model := deciderDefaults(w.strategy)
			return configform.Group{Fields: []configform.Field{
				{
					Title: titled("Decision service address", url),
					Value: orDefault(w.answers["decider-url"], url),
					Validate: func(a string) error {
						return check(map[string]string{"decider": w.strategy, "decider-url": a})
					},
				},
				{
					Title: titled("Decision service model", model),
					Value: orDefault(w.answers["decider-model"], model),
					Validate: func(a string) error {
						return check(map[string]string{"decider": w.strategy, "decider-model": a})
					},
				},
			}}
		},
		take: func(g configform.Group) {
			url, model := deciderDefaults(w.strategy)
			w.answers["decider-url"] = orEmpty(g.Fields[0].Value, url)
			w.answers["decider-model"] = orEmpty(g.Fields[1].Value, model)
		},
	}
}

// outputStep asks how much a run logs and how it draws its progress. Both are shared:
// they are written at the top level, where every command reads them.
func (w *wizard) outputStep() step {
	return step{
		group: func() configform.Group {
			return configform.Group{Fields: []configform.Field{
				{
					Title:   "How much should runs log?",
					Kind:    configform.Choice,
					Options: markDefault(configform.NewOptions(logLevels...), config.DefaultLogLevel),
					Value:   w.answers["log-level"],
				},
				{
					Title:   "How should runs show their progress on a terminal?",
					Kind:    configform.Choice,
					Options: markDefault(configform.NewOptions(progressModes...), config.DefaultProgress),
					Value:   w.answers["progress"],
				},
			}}
		},
		take: func(g configform.Group) {
			w.answers["log-level"] = g.Fields[0].Value
			w.answers["progress"] = g.Fields[1].Value
		},
	}
}

// run asks every step that applies, one form at a time.
func (w *wizard) run(ctx context.Context) error {
	for _, s := range w.steps() {
		if s.ask != nil && !s.ask() {
			continue
		}
		answered, err := configform.Run(ctx, w.screen, []configform.Group{s.group()})
		if err != nil {
			return err
		}
		s.take(answered[0])
	}
	return nil
}

// change is one edit the session makes to the file: a setting set to a new value, or
// removed so that it falls back to its default.
type change struct {
	Section string
	Setting setting
	From    string
	To      string
	Unset   bool
}

// changesBetween works out what a session changed, by the rule `config edit` follows:
// an answer that is still what the question started from is not written, and an
// answer that is the default removes the setting rather than pinning it — so a file
// the wizard made never freezes a user at today's defaults.
func changesBetween(start, end map[string]string) []change {
	var out []change
	for _, ws := range wizardSettings {
		from, to := start[ws.Flag], end[ws.Flag]
		if from == to {
			continue
		}
		out = append(out, change{
			Section: ws.Section,
			Setting: ws.setting(),
			From:    from,
			To:      to,
			Unset:   to == ws.defaultValue(),
		})
	}
	return out
}

// apply makes the changes to the document.
func apply(doc *configfile.Document, changes []change) error {
	for _, c := range changes {
		path := c.Setting.path(c.Section)
		if c.Unset {
			doc.Unset(path)
			continue
		}
		if err := doc.Set(path, valueNode(c.Setting, c.To)); err != nil {
			return err
		}
	}
	return nil
}

// decide ends the questions: it offers to remove any overrides, then lists the
// changes and asks to save them. It returns the changes to write, or nil when there is
// nothing to write — no change, or a no at the summary. A preview asks no confirmation:
// there is nothing to confirm.
//
// Everything the user has to read goes to stderr as plain lines before the question
// that depends on it. huh's accessible prompts print a field's title and nothing else,
// so a change list carried only in a field's help would never reach a screen reader.
func (w *wizard) decide(ctx context.Context, path string, dryRun bool) ([]change, error) {
	changes := changesBetween(w.start, w.answers)
	if overrides := w.overrides(); len(overrides) > 0 {
		remove, err := w.offerRemoval(ctx, overrides)
		if err != nil {
			return nil, err
		}
		if remove {
			changes = append(changes, overrides...)
		}
	}
	if len(changes) == 0 {
		_, _ = fmt.Fprintln(w.stderr, "nothing to change: the file already says what you answered")
		return nil, nil
	}

	printChanges(w.stderr, path, changes)
	if dryRun {
		return changes, nil
	}
	save, err := w.confirm(ctx, "Save these changes to "+path+"?", true)
	if err != nil {
		return nil, err
	}
	if !save {
		_, _ = fmt.Fprintln(w.stderr, "nothing was saved")
		return nil, nil
	}
	return changes, nil
}

// sharedAnswers are the answers written at the top level, which a command's own
// section can override.
var sharedAnswers = []string{"dest", "log-level", "progress"}

// overrides finds the settings a command's section holds that differ from a shared
// answer — each one means that answer does nothing for that command. They are offered
// for removal, as unset changes, and never removed without asking: they are settings
// the user wrote, and the wizard did not ask about them.
func (w *wizard) overrides() []change {
	if w.file == nil {
		return nil
	}
	own := map[string]configfile.Shared{
		sectionSort:  w.file.Sort.Shared,
		sectionClean: w.file.Clean.Shared,
		sectionUndo: {
			LogLevel: w.file.Undo.LogLevel, Output: w.file.Undo.Output, Progress: w.file.Undo.Progress,
		},
	}
	var out []change
	for _, section := range []string{sectionSort, sectionClean, sectionUndo} {
		for _, flag := range sharedAnswers {
			s, ok := lookupSetting(section, flag)
			if !ok {
				continue // undo has no dest
			}
			value, set := sharedValue(own[section], s.YAML)
			if !set || value == w.answers[flag] {
				continue
			}
			out = append(out, change{Section: section, Setting: s, From: value, To: w.answers[flag], Unset: true})
		}
	}
	return out
}

// offerRemoval lists the overrides and asks, defaulting to no, whether to remove them.
func (w *wizard) offerRemoval(ctx context.Context, overrides []change) (bool, error) {
	_, _ = fmt.Fprintln(w.stderr, "These settings in a command's own section override your answers for that command:")
	for _, c := range overrides {
		_, _ = fmt.Fprintf(w.stderr, "  %s: %s (you answered %s: %s)\n",
			c.Setting.key(c.Section), shown(c.From), c.Setting.YAML, shown(c.To))
	}
	remove, err := w.confirm(ctx, "Also remove these overrides?", false)
	if err != nil || remove {
		return remove, err
	}
	for _, c := range overrides {
		_, _ = fmt.Fprintf(w.stderr, "note: %s still overrides %s for %s\n",
			c.Setting.key(c.Section), c.Setting.YAML, c.Section)
	}
	return false, nil
}

// confirm asks one yes/no question.
func (w *wizard) confirm(ctx context.Context, title string, prefill bool) (bool, error) {
	answered, err := configform.Run(ctx, w.screen, []configform.Group{{
		Fields: []configform.Field{{Title: title, Kind: configform.Toggle, Value: strconv.FormatBool(prefill)}},
	}})
	if err != nil {
		return false, err
	}
	return answered[0].Fields[0].Value == "true", nil
}

// printChanges lists the changes, one line each, keyed the way `config show` keys
// them.
func printChanges(w io.Writer, path string, changes []change) {
	_, _ = fmt.Fprintf(w, "Changes to %s:\n", path)
	for _, c := range changes {
		key := c.Setting.key(c.Section)
		if c.Unset {
			_, _ = fmt.Fprintf(w, "  %s: %s → removed (falls back to %s)\n", key, shown(c.From), shown(c.To))
			continue
		}
		_, _ = fmt.Fprintf(w, "  %s: %s → %s\n", key, shown(c.From), shown(c.To))
	}
}

// shown spells a value for a summary line, naming the empty one.
func shown(value string) string {
	if value == "" {
		return "(unset)"
	}
	return value
}
