package cli_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sgaunet/moraine/internal/cli"
	"github.com/sgaunet/moraine/internal/config"
	"github.com/sgaunet/moraine/internal/configfile"
	"github.com/sgaunet/moraine/internal/exiftooltest"
)

// runWizard drives `config wizard` in accessible mode over a scripted standard input.
func runWizard(t *testing.T, script string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errs bytes.Buffer
	full := append([]string{"config", "wizard", "--accessible"}, args...)
	code = cli.ExecuteWithStdin("dev", full, strings.NewReader(script), &out, &errs)
	return out.String(), errs.String(), code
}

// newConfigPath returns where a test's configuration file lives, without creating it.
func newConfigPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "moraine.yaml")
}

// wizardField is one prompt of the wizard, in the order the wizard asks them. keep is
// the answer that leaves the prompt at its prefill, and asked says whether the prompt
// comes up when a test does not name it.
type wizardField struct {
	id    string
	keep  string
	asked func(a map[string]string) bool
}

func always(map[string]string) bool { return true }
func named(map[string]string) bool  { return false }

// wizardFields mirrors the wizard's step list (data-model.md). It is the one place the
// tests know the order of the questions, so a step added to the wizard is a line
// added here rather than a shifted answer in every script.
var wizardFields = []wizardField{
	{id: "dest", asked: always},
	{id: "themes", keep: "0", asked: always}, // a multiple choice is finished with 0
	{id: "custom-themes", asked: named},
	{id: "fallback", asked: always},
	{id: "layout", asked: always},
	{id: "template", asked: func(a map[string]string) bool { return a["layout"] == "4" }},
	{id: "sidecars", asked: always},
	{id: "classification", asked: always},
	{id: "model", asked: usesVisionModel},
	{id: "ollama-url", asked: usesVisionModel},
	{id: "decider-url", asked: usesDecider},
	{id: "decider-model", asked: usesDecider},
	{id: "log-level", asked: always},
	{id: "progress", asked: always},
	{id: "overrides", asked: named},
	{id: "confirm", asked: named},
}

// The classification step's options, by the number accessible mode answers them with.
const (
	classifyVision = "1"
	classifyLaya   = "2"
	classifyJev    = "3"
	classifyNone   = "4"
)

func usesVisionModel(a map[string]string) bool { return a["classification"] != classifyNone }

func usesDecider(a map[string]string) bool {
	return a["classification"] == classifyLaya || a["classification"] == classifyJev
}

// answers builds the standard input of a wizard session from the answers a test cares
// about, keyed by prompt id. A prompt it does not name keeps its prefill when it is
// asked; "-" marks a prompt the test expects not to be asked at all, for the cases the
// defaults above cannot see (a file that prefills "no model", for one).
func answers(t *testing.T, a map[string]string) string {
	t.Helper()
	var b strings.Builder
	for _, f := range wizardFields {
		v, ok := a[f.id]
		switch {
		case v == "-":
			continue
		case ok:
			b.WriteString(v + "\n")
		case f.asked(a):
			b.WriteString(f.keep + "\n")
		}
	}
	return b.String()
}

// readConfig decodes the file a session left behind, strictly, the way a run reads it.
func readConfig(t *testing.T, path string) *configfile.File {
	t.Helper()
	f, err := configfile.Read(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return f
}

// report decodes the stdout of a session run with --output=json.
type report struct {
	Path     string `json:"path"`
	Exists   bool   `json:"exists"`
	Written  bool   `json:"written"`
	DryRun   bool   `json:"dry_run"`
	Settings []struct {
		Key    string `json:"key"`
		Value  string `json:"value"`
		Origin string `json:"origin"`
	} `json:"settings"`
}

func decodeReport(t *testing.T, stdout string) report {
	t.Helper()
	var r report
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("stdout is not one JSON report: %v\n%s", err, stdout)
	}
	return r
}

// The form needs somewhere to draw. Without a terminal, and without the plain prompts
// that do not need one, the wizard refuses before asking anything — and says what
// would work instead.
func TestConfigWizardRefusesWithoutATerminal(t *testing.T) {
	var out, errs bytes.Buffer
	code := cli.ExecuteWithStdin("dev",
		[]string{"config", "wizard", "--config", newConfigPath(t)},
		strings.NewReader(""), &out, &errs)
	if code != 1 {
		t.Fatalf("exit = %d, want 1\n%s", code, errs.String())
	}
	for _, want := range []string{"--accessible", "config set"} {
		if !strings.Contains(errs.String(), want) {
			t.Errorf("stderr does not name %s:\n%s", want, errs.String())
		}
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", out.String())
	}
}

// The wizard spans every section by design, so a section argument is a usage error.
func TestConfigWizardTakesNoArguments(t *testing.T) {
	_, stderr, code := runWizard(t, "", "sort", "--config", newConfigPath(t))
	if code != 2 {
		t.Fatalf("exit = %d, want 2\n%s", code, stderr)
	}
}

// A file the wizard cannot read is one it must not overwrite: it refuses before the
// first question, and the file is left exactly as it was.
func TestConfigWizardRefusesAnUnreadableFile(t *testing.T) {
	const body = "sort:\n  not_a_setting: 1\n"
	path := writeConfig(t, body)
	_, stderr, code := runWizard(t, answers(t, nil), "--config", path)
	if code != 2 {
		t.Fatalf("exit = %d, want 2\n%s", code, stderr)
	}
	if got, _ := os.ReadFile(path); string(got) != body {
		t.Errorf("the file was changed:\n%s", got)
	}
}

// With MORAINE_CONFIG set to the empty string (which TestMain does for the whole
// suite) and no --config, there is no file to write, and that is a runtime failure.
func TestConfigWizardUnresolvablePath(t *testing.T) {
	_, stderr, code := runWizard(t, answers(t, nil))
	if code != 1 {
		t.Fatalf("exit = %d, want 1\n%s", code, stderr)
	}
}

// Accepting every answer changes nothing, so there is nothing to confirm and nothing
// to write — not even an empty file: a file of today's defaults would pin them.
func TestConfigWizardAcceptingEverythingWritesNothing(t *testing.T) {
	path := newConfigPath(t)
	stdout, stderr, code := runWizard(t, answers(t, nil), "--config", path, "--output=json")
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, stderr)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a file was created: %v", err)
	}
	if !strings.Contains(stderr, "nothing to change") {
		t.Errorf("stderr does not say there was nothing to change:\n%s", stderr)
	}
	if r := decodeReport(t, stdout); r.Written || r.Exists {
		t.Errorf("report = written=%v exists=%v, want neither", r.Written, r.Exists)
	}
}

// The prompts a US1 session answers, by the number accessible mode answers them with.
const (
	themeCook    = "3" // mountain, special-events, cook, family, then the custom option
	themeCustom  = "5"
	layoutYearly = "2" // {year}/{month}/{theme}
	layoutCustom = "4"
	levelWarn    = "3" // debug, info, warn, error
	progressNvr  = "3" // auto, always, never
)

// The whole story in one test: a new user changes the destination and drops one
// theme, and the file that results says exactly that — nothing about the questions
// they merely accepted. The file's directory does not exist yet; saving creates it.
func TestConfigWizardFirstRunWritesOnlyTheChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new", "dir", "moraine.yaml")
	lib := filepath.Join(t.TempDir(), "lib")
	_, stderr, code := runWizard(t,
		answers(t, map[string]string{"dest": lib, "themes": themeCook + "\n0", "confirm": "y"}),
		"--config", path)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, stderr)
	}

	f := readConfig(t, path)
	if f.Dest == nil || *f.Dest != lib {
		t.Errorf("dest = %v, want %s", f.Dest, lib)
	}
	if got, want := strings.Join(f.Sort.Themes, ","), "mountain,special-events,family"; got != want {
		t.Errorf("themes = %s, want %s", got, want)
	}
	f.Dest, f.Sort.Themes = nil, nil
	if !reflect.DeepEqual(*f, configfile.File{}) {
		t.Errorf("the file sets more than the two changes: %+v", *f)
	}
}

// Declining at the summary is how a session is abandoned on purpose: nothing is
// written, not even the directory the file would have gone in, and that is not an
// error.
func TestConfigWizardDeclineWritesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never")
	path := filepath.Join(dir, "moraine.yaml")
	_, stderr, code := runWizard(t,
		answers(t, map[string]string{"themes": themeCook + "\n0", "confirm": "n"}),
		"--config", path)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, stderr)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the file's directory was created: %v", err)
	}
	if !strings.Contains(stderr, "nothing was saved") {
		t.Errorf("stderr does not say nothing was saved:\n%s", stderr)
	}
}

// A file that cannot be saved is a runtime failure, reported on stderr with stdout
// left empty — a script reading the settings must not be handed a report of a write
// that never happened.
func TestConfigWizardSaveFailureIsARuntimeError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through a read-only directory")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	path := filepath.Join(dir, "moraine.yaml")

	stdout, stderr, code := runWizard(t,
		answers(t, map[string]string{"themes": themeCook + "\n0", "confirm": "y"}),
		"--config", path)
	if code != 1 {
		t.Fatalf("exit = %d, want 1\n%s", code, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
	if !strings.Contains(stderr, dir) {
		t.Errorf("stderr does not name where it failed to write:\n%s", stderr)
	}
}

// Text typed into a form is never expanded by a shell, so "~" would otherwise reach
// the file literally and become a directory named "~" wherever sort was run. The
// wizard expands it, and refuses a relative path for the same reason.
func TestConfigWizardDestination(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	t.Run("tilde is expanded", func(t *testing.T) {
		path := newConfigPath(t)
		_, stderr, code := runWizard(t,
			answers(t, map[string]string{"dest": "~/sorted", "confirm": "y"}), "--config", path)
		if code != 0 {
			t.Fatalf("exit = %d\n%s", code, stderr)
		}
		f := readConfig(t, path)
		if want := filepath.Join(home, "sorted"); f.Dest == nil || *f.Dest != want {
			t.Errorf("dest = %v, want %s", f.Dest, want)
		}
		if raw, _ := os.ReadFile(path); strings.Contains(string(raw), "~") {
			t.Errorf("the file still holds a ~:\n%s", raw)
		}
	})

	t.Run("a relative path is refused at the question", func(t *testing.T) {
		path := newConfigPath(t)
		abs := filepath.Join(home, "lib")
		_, stderr, code := runWizard(t,
			answers(t, map[string]string{"dest": "rel/lib\n" + abs, "confirm": "y"}), "--config", path)
		if code != 0 {
			t.Fatalf("exit = %d\n%s", code, stderr)
		}
		if !strings.Contains(stderr, "absolute") {
			t.Errorf("the refusal does not say why:\n%s", stderr)
		}
		if f := readConfig(t, path); f.Dest == nil || *f.Dest != abs {
			t.Errorf("dest = %v, want the second, absolute answer %s", f.Dest, abs)
		}
	})
}

// Themes are picked from a list, and a user's own are added only by asking for them.
func TestConfigWizardThemes(t *testing.T) {
	t.Run("picking none is refused", func(t *testing.T) {
		path := newConfigPath(t)
		// Untick all four, try to finish, then tick mountain back and finish.
		_, stderr, code := runWizard(t,
			answers(t, map[string]string{"themes": "1\n2\n3\n4\n0\n1\n0", "confirm": "y"}), "--config", path)
		if code != 0 {
			t.Fatalf("exit = %d\n%s", code, stderr)
		}
		if !strings.Contains(stderr, "at least one theme") {
			t.Errorf("the refusal was never shown:\n%s", stderr)
		}
		if got := readConfig(t, path).Sort.Themes; len(got) != 1 || got[0] != "mountain" {
			t.Errorf("themes = %v, want [mountain]", got)
		}
	})

	t.Run("custom themes are added to the picks", func(t *testing.T) {
		path := newConfigPath(t)
		_, stderr, code := runWizard(t, answers(t, map[string]string{
			"themes": themeCustom + "\n0", "custom-themes": "Bad Theme\nboats, cook", "confirm": "y",
		}), "--config", path)
		if code != 0 {
			t.Fatalf("exit = %d\n%s", code, stderr)
		}
		if !strings.Contains(stderr, "Bad Theme") {
			t.Errorf("the invalid theme was not refused:\n%s", stderr)
		}
		got := strings.Join(readConfig(t, path).Sort.Themes, ",")
		if want := "mountain,special-events,cook,family,boats"; got != want {
			t.Errorf("themes = %s, want %s (picked, then the new ones, without duplicates)", got, want)
		}
	})

	t.Run("custom themes are not asked unless wanted", func(t *testing.T) {
		_, stderr, _ := runWizard(t, answers(t, nil), "--config", newConfigPath(t))
		if strings.Contains(stderr, customThemesTitle) {
			t.Errorf("the custom-themes question was asked:\n%s", stderr)
		}
	})
}

// customThemesTitle is the start of the question that asks for a user's own themes.
const customThemesTitle = "Your own themes"

// The fallback theme is checked against the themes just chosen, not the ones the file
// held before the session.
func TestConfigWizardFallbackCannotBeATheme(t *testing.T) {
	path := newConfigPath(t)
	_, stderr, code := runWizard(t, answers(t, map[string]string{
		"themes": themeCustom + "\n0", "custom-themes": "misc", "fallback": "misc\nleftovers", "confirm": "y",
	}), "--config", path)
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "cannot be the same as the fallback") {
		t.Errorf("a fallback that is also a theme was not refused:\n%s", stderr)
	}
	if f := readConfig(t, path); f.Sort.FallbackTheme == nil || *f.Sort.FallbackTheme != "leftovers" {
		t.Errorf("fallback = %v, want leftovers", f.Sort.FallbackTheme)
	}
}

// Folder layouts are offered by name with an example path each, and a custom
// template is checked the way a run checks it.
func TestConfigWizardLayout(t *testing.T) {
	t.Run("a preset writes its template, with an example shown", func(t *testing.T) {
		path := newConfigPath(t)
		_, stderr, code := runWizard(t,
			answers(t, map[string]string{"layout": layoutYearly, "confirm": "y"}), "--config", path)
		if code != 0 {
			t.Fatalf("exit = %d\n%s", code, stderr)
		}
		if !strings.Contains(stderr, "2025/07/mountain") {
			t.Errorf("the preset's example path is not shown:\n%s", stderr)
		}
		if f := readConfig(t, path); f.Sort.PathTemplate == nil || *f.Sort.PathTemplate != "{year}/{month}/{theme}" {
			t.Errorf("path_template = %v", f.Sort.PathTemplate)
		}
	})

	t.Run("a custom template is validated", func(t *testing.T) {
		path := newConfigPath(t)
		_, stderr, code := runWizard(t, answers(t, map[string]string{
			"layout": layoutCustom, "template": "{bogus}\n{theme}/{year}", "confirm": "y",
		}), "--config", path)
		if code != 0 {
			t.Fatalf("exit = %d\n%s", code, stderr)
		}
		if !strings.Contains(stderr, "bogus") {
			t.Errorf("the bad template was not refused:\n%s", stderr)
		}
		if f := readConfig(t, path); f.Sort.PathTemplate == nil || *f.Sort.PathTemplate != "{theme}/{year}" {
			t.Errorf("path_template = %v", f.Sort.PathTemplate)
		}
	})
}

// Companion files are sort's own setting; log level and progress are shared, so they
// go to the top level, where every command inherits them.
func TestConfigWizardSidecarsAndOutputSettings(t *testing.T) {
	path := newConfigPath(t)
	_, stderr, code := runWizard(t, answers(t, map[string]string{
		"sidecars": "n", "log-level": levelWarn, "progress": progressNvr, "confirm": "y",
	}), "--config", path)
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, stderr)
	}
	f := readConfig(t, path)
	if f.Sort.Sidecars == nil || *f.Sort.Sidecars {
		t.Errorf("sort.sidecars = %v, want false", f.Sort.Sidecars)
	}
	if f.LogLevel == nil || *f.LogLevel != "warn" || f.Progress == nil || *f.Progress != "never" {
		t.Errorf("top level: log_level=%v progress=%v, want warn and never", f.LogLevel, f.Progress)
	}
	if f.Sort.LogLevel != nil || f.Sort.Progress != nil {
		t.Error("a shared setting was written into the sort section")
	}
}

// A preview asks every question, shows the changes, and writes nothing: there is
// nothing to confirm.
func TestConfigWizardDryRunWritesNothing(t *testing.T) {
	path := newConfigPath(t)
	stdout, stderr, code := runWizard(t,
		answers(t, map[string]string{"themes": themeCook + "\n0"}), "--config", path, "-n", "--output=json")
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, stderr)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a preview wrote the file: %v", err)
	}
	if strings.Contains(stderr, "Save these changes") {
		t.Error("a preview asked for confirmation")
	}
	if !strings.Contains(stderr, "sort.themes") {
		t.Errorf("the change list was not shown:\n%s", stderr)
	}
	r := decodeReport(t, stdout)
	if !r.DryRun || r.Written {
		t.Errorf("report: dry_run=%v written=%v", r.DryRun, r.Written)
	}
	for _, s := range r.Settings {
		if s.Key == "sort.themes" && s.Value != "mountain,special-events,family" {
			t.Errorf("sort.themes = %s, want the would-be value", s.Value)
		}
	}
}

// Stdout carries the settings and nothing else, so the wizard can run with its
// result redirected: every question goes to stderr.
func TestConfigWizardKeepsTheFormOffStdout(t *testing.T) {
	stdout, stderr, code := runWizard(t,
		answers(t, map[string]string{"themes": themeCook + "\n0", "confirm": "y"}),
		"--config", newConfigPath(t), "--output=json")
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, stderr)
	}
	if r := decodeReport(t, stdout); !r.Written {
		t.Error("report does not say the file was written")
	}
	if !strings.Contains(stderr, "Save these changes") {
		t.Error("the questions did not go to stderr")
	}
}

// A file the wizard saves is one a real run accepts, not merely one that decodes.
func TestConfigWizardSavedFileIsValid(t *testing.T) {
	path := newConfigPath(t)
	_, stderr, code := runWizard(t, answers(t, map[string]string{
		"dest":     filepath.Join(t.TempDir(), "lib"),
		"themes":   themeCook + "\n0",
		"fallback": "misc",
		"layout":   layoutYearly,
		"sidecars": "n",
		"confirm":  "y",
	}), "--config", path)
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, stderr)
	}

	var out, errs bytes.Buffer
	if code := cli.Execute("dev", []string{"config", "show", "--config", path}, &out, &errs); code != 0 {
		t.Fatalf("config show: exit = %d\n%s", code, errs.String())
	}

	src := t.TempDir()
	writePNG(t, filepath.Join(src, "a.png"))
	exifPath, err := exiftooltest.Stub(t.TempDir(), exiftooltest.Options{})
	if err != nil {
		t.Fatal(err)
	}
	errs.Reset()
	code = cli.Execute("dev", []string{
		"sort", "--dry-run", "--sample", "0", "--exiftool", exifPath, "--config", path, src,
	}, io.Discard, &errs)
	if code != 0 {
		t.Fatalf("sort rejected the file the wizard saved: exit = %d\n%s", code, errs.String())
	}
}

// Titles of the classification follow-ups, as a session prints them.
const (
	modelTitle      = "Vision model"
	deciderURLTitle = "Decision service address"
)

// The default — the local vision model — asks about that model and nothing about
// decision services.
func TestConfigWizardVisionModelAsksNoDeciderQuestions(t *testing.T) {
	_, stderr, code := runWizard(t, answers(t, nil), "--config", newConfigPath(t))
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, stderr)
	}
	if !strings.Contains(stderr, modelTitle+" (default: qwen3-vl:8b)") {
		t.Errorf("the model question, with its default, was not asked:\n%s", stderr)
	}
	if strings.Contains(stderr, deciderURLTitle) {
		t.Errorf("a decision-service question was asked:\n%s", stderr)
	}
}

// Choosing a decision service writes the choice, never a credential. The key's
// variable is named so the user knows where it goes, but its value is never read
// into the file or echoed back — even when it is set in the wizard's environment.
func TestConfigWizardDeciderChoices(t *testing.T) {
	for _, tc := range []struct {
		name, choice, backend, keyVar string
		keySet                        bool
	}{
		{"laya", classifyLaya, "laya", "LAYA_API_KEY", true},
		{"jev", classifyJev, "jev", "TYPESAFE_API_KEY", true},
		{"jev without its key", classifyJev, "jev", "TYPESAFE_API_KEY", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const sentinel = "sk-must-never-appear"
			if tc.keySet {
				t.Setenv(tc.keyVar, sentinel)
			} else {
				t.Setenv(tc.keyVar, "")
			}
			path := newConfigPath(t)
			stdout, stderr, code := runWizard(t,
				answers(t, map[string]string{"classification": tc.choice, "confirm": "y"}), "--config", path)
			if code != 0 {
				t.Fatalf("exit = %d\n%s", code, stderr)
			}

			f := readConfig(t, path)
			if f.Sort.Decider == nil || *f.Sort.Decider != tc.backend {
				t.Errorf("sort.decider = %v, want %s", f.Sort.Decider, tc.backend)
			}
			if f.Sort.DeciderURL != nil || f.Sort.DeciderModel != nil {
				t.Errorf("the backend's defaults were pinned: url=%v model=%v",
					f.Sort.DeciderURL, f.Sort.DeciderModel)
			}
			if !strings.Contains(stderr, tc.keyVar) {
				t.Errorf("stderr does not name %s:\n%s", tc.keyVar, stderr)
			}
			raw, _ := os.ReadFile(path)
			for where, text := range map[string]string{"file": string(raw), "stdout": stdout, "stderr": stderr} {
				if strings.Contains(text, sentinel) {
					t.Errorf("the key's value appears on %s", where)
				}
			}
		})
	}
}

// "No model" is sample 0, asks nothing about models, and clears the decision-service
// settings a previous choice left behind: inert keys in a file only mislead.
func TestConfigWizardNoModel(t *testing.T) {
	path := writeConfig(t, "sort:\n  decider: laya\n  decider_url: http://laya.lan:8000\n")
	_, stderr, code := runWizard(t,
		answers(t, map[string]string{"classification": classifyNone, "confirm": "y"}), "--config", path)
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, stderr)
	}
	if strings.Contains(stderr, modelTitle) {
		t.Errorf("a model question was asked for no model:\n%s", stderr)
	}
	f := readConfig(t, path)
	if f.Sort.Sample == nil || *f.Sort.Sample != 0 {
		t.Errorf("sort.sample = %v, want 0", f.Sort.Sample)
	}
	if f.Sort.Decider != nil || f.Sort.DeciderURL != nil {
		t.Errorf("decision-service settings survived: decider=%v url=%v", f.Sort.Decider, f.Sort.DeciderURL)
	}
}

// The wizard contacts nothing: a wrong address shows up on the first sort, which
// already degrades with a warning. Both addresses here point at a server that fails
// the test if it is ever asked anything.
func TestConfigWizardContactsNoService(t *testing.T) {
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		asked.Add(1)
	}))
	t.Cleanup(srv.Close)

	path := writeConfig(t, "sort:\n  ollama_url: "+srv.URL+"\n  decider: laya\n  decider_url: "+srv.URL+"\n")
	_, stderr, code := runWizard(t,
		answers(t, map[string]string{"classification": classifyLaya, "themes": themeCook + "\n0", "confirm": "y"}),
		"--config", path)
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, stderr)
	}
	if n := asked.Load(); n != 0 {
		t.Errorf("the wizard sent %d request(s)", n)
	}
}

// A re-run changes what was answered differently and nothing else: the comments the
// user wrote and the settings the wizard does not ask about stay where they were.
func TestConfigWizardRerunKeepsCommentsAndUnrelatedKeys(t *testing.T) {
	path := writeConfig(t, "# my photo setup\nsort:\n  jobs: 2 # NAS is slow\n  themes: [mountain, cook]\n")
	_, stderr, code := runWizard(t,
		answers(t, map[string]string{"themes": "4\n0", "confirm": "y"}), // tick family
		"--config", path)
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, stderr)
	}
	raw, _ := os.ReadFile(path)
	for _, want := range []string{"# my photo setup", "jobs: 2 # NAS is slow"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the file lost %q:\n%s", want, raw)
		}
	}
	if got := strings.Join(readConfig(t, path).Sort.Themes, ","); got != "mountain,cook,family" {
		t.Errorf("themes = %s, want mountain,cook,family", got)
	}
}

// Every question starts from what the file says, so accepting them all on a file the
// wizard did not write changes nothing — the proof that each prefill is faithful,
// including those worked out rather than read (a layout, "no model").
func TestConfigWizardPrefillsFromTheFile(t *testing.T) {
	for name, body := range map[string]string{
		"a preset layout, no model, a custom theme": "sort:\n  themes: [mountain, boats]\n" +
			"  fallback_theme: misc\n  path_template: '{year}/{month}/{theme}'\n  sample: 0\n",
		"a template of one's own": "sort:\n  path_template: '{theme}/{year}/{month}'\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := writeConfig(t, body)
			a := map[string]string{}
			if strings.Contains(body, "sample: 0") {
				a["model"], a["ollama-url"] = "-", "-" // not asked: the file says no model
			}
			if strings.Contains(body, "{month}'") && !strings.Contains(body, "{year}/{month}/{theme}") {
				a["template"] = "" // asked: the layout is prefilled as a template of one's own
			}
			_, stderr, code := runWizard(t, answers(t, a), "--config", path)
			if code != 0 {
				t.Fatalf("exit = %d\n%s", code, stderr)
			}
			if !strings.Contains(stderr, "nothing to change") {
				t.Errorf("accepting every prefill changed something:\n%s", stderr)
			}
			if raw, _ := os.ReadFile(path); string(raw) != body {
				t.Errorf("the file was rewritten:\n%s", raw)
			}
		})
	}

	t.Run("answering the default removes the setting", func(t *testing.T) {
		path := writeConfig(t, "sort:\n  fallback_theme: misc\n")
		_, stderr, code := runWizard(t,
			answers(t, map[string]string{"fallback": config.DefaultFallback, "confirm": "y"}), "--config", path)
		if code != 0 {
			t.Fatalf("exit = %d\n%s", code, stderr)
		}
		if f := readConfig(t, path); f.Sort.FallbackTheme != nil {
			t.Errorf("fallback_theme = %s, want it removed", *f.Sort.FallbackTheme)
		}
	})
}

// Accessible mode prints a question's title and nothing else, so a default the user
// must see is in the title.
func TestConfigWizardShowsEachDefault(t *testing.T) {
	_, stderr, _ := runWizard(t, answers(t, nil), "--config", newConfigPath(t))
	for _, want := range []string{
		"(default: <source>/_sorted)",
		"Theme for photos no other theme fits (default: other)",
		"Vision model (default: qwen3-vl:8b)",
		"info (default)",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr does not show %q", want)
		}
	}
}

// A command's own section can override a shared answer, which would then do nothing
// for that command. The wizard names each such override and removes them only when
// asked to — they are settings the user wrote, not ones it did.
func TestConfigWizardOverrides(t *testing.T) {
	const body = "clean:\n  log_level: debug\nsort:\n  dest: /old\n"
	newDest := filepath.Join(t.TempDir(), "new")

	t.Run("kept unless removal is asked for", func(t *testing.T) {
		path := writeConfig(t, body)
		_, stderr, code := runWizard(t,
			answers(t, map[string]string{"dest": newDest, "overrides": "n", "confirm": "y"}), "--config", path)
		if code != 0 {
			t.Fatalf("exit = %d\n%s", code, stderr)
		}
		if !strings.Contains(stderr, "sort.dest") || !strings.Contains(stderr, "still overrides") {
			t.Errorf("the override was not reported:\n%s", stderr)
		}
		f := readConfig(t, path)
		if f.Sort.Dest == nil || *f.Sort.Dest != "/old" || f.Clean.LogLevel == nil {
			t.Errorf("an override was removed without asking: sort.dest=%v clean.log_level=%v",
				f.Sort.Dest, f.Clean.LogLevel)
		}
	})

	t.Run("removed when asked", func(t *testing.T) {
		path := writeConfig(t, body)
		_, stderr, code := runWizard(t,
			answers(t, map[string]string{"dest": newDest, "overrides": "y", "confirm": "y"}), "--config", path)
		if code != 0 {
			t.Fatalf("exit = %d\n%s", code, stderr)
		}
		f := readConfig(t, path)
		if f.Sort.Dest != nil || f.Clean.LogLevel != nil {
			t.Errorf("overrides survived: sort.dest=%v clean.log_level=%v", f.Sort.Dest, f.Clean.LogLevel)
		}
		if f.Dest == nil || *f.Dest != newDest {
			t.Errorf("dest = %v, want %s", f.Dest, newDest)
		}
	})

	t.Run("not asked when there are none", func(t *testing.T) {
		_, stderr, _ := runWizard(t,
			answers(t, map[string]string{"dest": newDest, "confirm": "n"}), "--config", newConfigPath(t))
		if strings.Contains(stderr, overridesTitle) {
			t.Errorf("the override question was asked with no overrides:\n%s", stderr)
		}
	})
}

// overridesTitle is the question that offers to remove overrides.
const overridesTitle = "Also remove these overrides?"

// The session contracts/cli.md shows, typed exactly as it is printed there — the one
// script in this file written by position rather than through answers(), so that it
// pins accessible mode's encoding end to end.
func TestConfigWizardContractTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := newConfigPath(t)
	script := "~/Pictures/sorted\n3\n0\n" + strings.Repeat("\n", 8) + "y\n"
	_, stderr, code := runWizard(t, script, "--config", path)
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, stderr)
	}
	f := readConfig(t, path)
	if want := filepath.Join(home, "Pictures", "sorted"); f.Dest == nil || *f.Dest != want {
		t.Errorf("dest = %v, want %s", f.Dest, want)
	}
	if got := strings.Join(f.Sort.Themes, ","); got != "mountain,special-events,family" {
		t.Errorf("themes = %s", got)
	}
	raw, _ := os.ReadFile(path)
	for _, key := range []string{"dry_run", "delete", "incremental", "move", "quiet", "verbose"} {
		if strings.Contains(string(raw), key) {
			t.Errorf("the wizard wrote the unconfigurable %s:\n%s", key, raw)
		}
	}
}

// questionTitles are the start of every question the wizard can ask, before the
// summary.
var questionTitles = []string{
	"Where should sorted copies go?", "Which themes should photos be sorted into?",
	customThemesTitle, "Theme for photos no other theme fits", "How should folders be laid out?",
	"Folder template", "Copy companion files", "How should events be recognised?",
	modelTitle, "Ollama address", deciderURLTitle, "Decision service model",
	"How much should runs log?", "How should runs show their progress",
}

// questionsBeforeSummary counts the questions a session asked.
func questionsBeforeSummary(stderr string) int {
	n := 0
	for _, title := range questionTitles {
		n += strings.Count(stderr, title)
	}
	return n
}

// The wizard is short by contract: 10 questions on the path a first-time user takes,
// and 14 on the longest one. A question added later fails here rather than quietly
// lengthening every first run.
func TestConfigWizardQuestionBudget(t *testing.T) {
	for _, tc := range []struct {
		name string
		a    map[string]string
		want int
	}{
		{"default path", map[string]string{"dest": "/tmp/lib", "themes": themeCook + "\n0", "confirm": "n"}, 10},
		{"longest path", map[string]string{
			"themes": themeCustom + "\n0", "custom-themes": "boats",
			"layout": layoutCustom, "template": "{theme}/{year}",
			"classification": classifyJev, "confirm": "n",
		}, 14},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, code := runWizard(t, answers(t, tc.a), "--config", newConfigPath(t))
			if code != 0 {
				t.Fatalf("exit = %d\n%s", code, stderr)
			}
			if got := questionsBeforeSummary(stderr); got != tc.want {
				t.Errorf("asked %d questions, want %d", got, tc.want)
			}
		})
	}
}

// The help is where a user learns when to reach for the wizard rather than edit or
// set, that it only goes forward, and what each exit code means.
func TestConfigWizardHelp(t *testing.T) {
	var out bytes.Buffer
	cli.Execute("dev", []string{"config", "wizard", "--help"}, &out, io.Discard)
	for _, want := range []string{
		"config edit", "config set", "run it again", "LAYA_API_KEY", "TYPESAFE_API_KEY",
		"--accessible", "--dry-run", "Exit codes:", "0  success", "1  runtime failure", "2  usage error",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("config wizard --help does not mention %q", want)
		}
	}

	out.Reset()
	cli.Execute("dev", []string{"config", "--help"}, &out, io.Discard)
	for _, want := range []string{"wizard ", "moraine config wizard"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("config --help does not mention %q", want)
		}
	}
}
