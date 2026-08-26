package cli_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/sgaunet/moraine/internal/cli"
)

// runEdit drives `config edit` in accessible mode over a scripted standard input,
// which is the only way to answer a form without a terminal.
//
// The script answers two forms in a row: first the picker — a number per setting to
// pick, then 0 to move on — and then one answer per setting picked.
func runEdit(t *testing.T, script string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errs bytes.Buffer
	full := append([]string{"config", "edit"}, args...)
	code = cli.ExecuteWithStdin("dev", append(full, "--accessible"),
		strings.NewReader(script), &out, &errs)
	return out.String(), errs.String(), code
}

// questionsAsked counts the value questions a session asked about the named settings.
//
// A question prints its title on a line of its own, while the picker lists the same
// setting indented behind a number and followed by its value — so an exact match on
// the trimmed line tells the two apart. (huh's accessible prompts render only a
// field's title, never its description, which is why the "(default: …)" help line is
// not what to look for here.)
func questionsAsked(stderr string, keys ...string) int {
	n := 0
	for _, line := range strings.Split(stderr, "\n") {
		if slices.Contains(keys, strings.TrimSpace(line)) {
			n++
		}
	}
	return n
}

// The defect this shape exists to prevent: a form that asked about every setting could
// only be submitted from its last question, so saving one change meant pressing enter
// through two dozen of them. Picking first means the form is as long as the change.
func TestConfigEditOnlyAsksAboutWhatWasPicked(t *testing.T) {
	configAt(t, "")
	// Pick the first of undo's two settings, then 0 to move on; answer it with the
	// third option (warn).
	_, stderr, code := runEdit(t, "1\n0\n3\n", "undo")
	if code != 0 {
		t.Fatalf("exit = %d, stderr:\n%s", code, stderr)
	}
	if n := questionsAsked(stderr, "undo.log_level", "undo.output"); n != 1 {
		t.Errorf("asked %d questions for one setting picked; want 1\n%s", n, stderr)
	}
}

// Picking nothing is a legitimate answer — the way to back out — not a failure, and it
// must leave the file exactly as it was. Here that means not creating one at all.
func TestConfigEditPickingNothingChangesNothing(t *testing.T) {
	path := configAt(t, "")

	stdout, stderr, code := runEdit(t, "0\n", "undo")
	if code != 0 {
		t.Fatalf("exit = %d, stderr:\n%s", code, stderr)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("picking nothing created a configuration file")
	}
	if questionsAsked(stderr, "undo.log_level", "undo.output") != 0 {
		t.Errorf("questions were asked about settings nobody picked:\n%s", stderr)
	}
	if !strings.Contains(stdout, "undo.log_level=info origin=default") {
		t.Errorf("stdout should still report the effective settings:\n%s", stdout)
	}
}

// The requirement, end to end: a question starts from the value in effect, so
// accepting it unchanged leaves it unchanged — and in particular does not stamp
// today's defaults into the file, which would freeze this user at them.
func TestConfigEditPrefillsTheValueInEffect(t *testing.T) {
	path := configAt(t, "undo:\n  log_level: error\n")

	// Pick log_level, then answer with an empty line: keep what it already is.
	stdout, stderr, code := runEdit(t, "1\n0\n\n", "undo")
	if code != 0 {
		t.Fatalf("exit = %d, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "undo.log_level=error origin=file") {
		t.Errorf("the file's value was not carried through:\n%s", stdout)
	}
	if body := read(t, path); !strings.Contains(body, "log_level: error") {
		t.Errorf("the file changed although nothing was answered differently:\n%s", body)
	}
}

// The picker doubles as a way to find a setting, so it shows the value in effect and
// marks the ones the file sets.
func TestConfigEditListsCurrentValues(t *testing.T) {
	configAt(t, "undo:\n  log_level: error\n")
	_, stderr, code := runEdit(t, "0\n", "undo")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	for _, want := range []string{"undo.log_level", "error", "undo.output", "text"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the picker does not show %q:\n%s", want, stderr)
		}
	}
}

// An answer that differs is written; the settings left alone are not.
func TestConfigEditWritesOnlyWhatChanged(t *testing.T) {
	path := configAt(t, "")
	// Pick log_level (1), move on (0), answer with option 3 = warn.
	if _, stderr, code := runEdit(t, "1\n0\n3\n", "undo"); code != 0 {
		t.Fatalf("exit = %d, stderr:\n%s", code, stderr)
	}
	body := read(t, path)
	if !strings.Contains(body, "log_level: warn") {
		t.Errorf("the changed setting was not written:\n%s", body)
	}
	if strings.Contains(body, "output:") {
		t.Errorf("a setting nobody picked was written too:\n%s", body)
	}
}

// Answering with the default again is how a setting is taken back, so it removes the
// key rather than pinning it — the same result as `config unset`.
func TestConfigEditAnsweringTheDefaultRemovesTheSetting(t *testing.T) {
	path := configAt(t, "undo:\n  log_level: warn\n")
	// Pick log_level, then answer option 2 = info, which is the default.
	if _, stderr, code := runEdit(t, "1\n0\n2\n", "undo"); code != 0 {
		t.Fatalf("exit = %d, stderr:\n%s", code, stderr)
	}
	if body := read(t, path); strings.Contains(body, "log_level") {
		t.Errorf("answering the default should remove the setting, got:\n%s", body)
	}
}

// The form must never touch stdout: stdout carries the result, so a redirect stays a
// clean document while the questions are still on screen (Principle V).
func TestConfigEditKeepsTheFormOffStdout(t *testing.T) {
	configAt(t, "")
	stdout, stderr, code := runEdit(t, "1\n0\n3\n", "undo", "--output=json")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var doc struct {
		Command string `json:"command"`
		Written bool   `json:"written"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not a clean JSON document: %v\n%s", err, stdout)
	}
	if doc.Command != "config" || !doc.Written {
		t.Errorf("unexpected document: %+v", doc)
	}
	if !strings.Contains(stderr, "undo.log_level") {
		t.Errorf("the questions should have gone to stderr:\n%s", stderr)
	}
}

// An answer no run could use is refused as the question is answered, rather than at
// the end when a whole session's typing would be lost.
func TestConfigEditRefusesABadAnswerAtTheQuestion(t *testing.T) {
	path := configAt(t, "")
	// The picker numbers a section's settings in the order the table lists them, so
	// --gap is found rather than counted to: adding a shared setting would otherwise
	// silently move it and this test would answer a different question. Answer it with
	// a non-duration, which must be refused, then with a real one.
	pick := slices.Index(cli.SettingFlags("sort"), "gap") + 1
	if pick == 0 {
		t.Fatal("sort has no gap setting")
	}
	stdout, stderr, code := runEdit(t, fmt.Sprintf("%d\n0\nnope\n12h\n", pick), "sort")
	if code != 0 {
		t.Fatalf("exit = %d, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "invalid duration") {
		t.Errorf("the bad answer was not refused at the question:\n%s", stderr)
	}
	if !strings.Contains(stdout, "sort.gap=12h origin=file") {
		t.Errorf("the second, valid answer did not take:\n%s", stdout)
	}
	if body := read(t, path); !strings.Contains(body, "gap: 12h") {
		t.Errorf("file:\n%s", body)
	}
}

// A full-screen form needs a terminal. Without one, say so and name the two ways
// forward rather than drawing into a pipe (Principle V).
func TestConfigEditRefusesWithoutATerminal(t *testing.T) {
	configAt(t, "")
	var out, errs bytes.Buffer
	code := cli.ExecuteWithStdin("dev", []string{"config", "edit", "undo"},
		strings.NewReader(""), &out, &errs)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	for _, want := range []string{"--accessible", "config set"} {
		if !strings.Contains(errs.String(), want) {
			t.Errorf("the refusal should mention %q, got:\n%s", want, errs.String())
		}
	}
	if out.Len() != 0 {
		t.Errorf("nothing should have reached stdout:\n%s", out.String())
	}
}

// --dry-run answers the questions and reports the outcome without writing.
func TestConfigEditDryRunWritesNothing(t *testing.T) {
	path := configAt(t, "")
	stdout, _, code := runEdit(t, "1\n0\n3\n", "undo", "--dry-run")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stdout, "undo.log_level=warn origin=file") {
		t.Errorf("the answer was not reported:\n%s", stdout)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a dry run created the configuration file")
	}
}

// With no section named, the picker covers every one of them.
func TestConfigEditWithoutASectionCoversAllOfThem(t *testing.T) {
	configAt(t, "")
	stdout, stderr, code := runEdit(t, "0\n")
	if code != 0 {
		t.Fatalf("exit = %d, stderr:\n%s", code, stderr)
	}
	for _, want := range []string{"sort.gap", "clean.dest", "undo.output"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the picker does not offer %q:\n%s", want, stderr)
		}
	}
	for _, want := range []string{"log_level=", "sort.gap=", "clean.dest=", "undo.output="} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout does not cover %q:\n%s", want, stdout)
		}
	}
}

// An unknown section is a usage error, before any question is asked.
func TestConfigEditRefusesAnUnknownSection(t *testing.T) {
	configAt(t, "")
	if _, _, code := runEdit(t, "", "nonsense"); code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
}
