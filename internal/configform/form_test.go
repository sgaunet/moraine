package configform_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/sgaunet/moraine/internal/configform"
)

// groups builds a form covering all three kinds of field, prefilled the way the
// caller prefills it: with the value in effect today.
func groups() []configform.Group {
	return []configform.Group{{
		Title: "sort",
		Fields: []configform.Field{
			{Title: "--gap", Help: "max time gap within an event", Value: "6h"},
			{Title: "--log-level", Kind: configform.Choice, Options: configform.NewOptions("debug", "info", "warn", "error"), Value: "info"},
			{Title: "--vote", Kind: configform.Toggle, Value: "false"},
		},
	}}
}

// run drives the form in accessible mode over a scripted stdin.
func run(t *testing.T, script string, in []configform.Group) ([]configform.Group, error) {
	t.Helper()
	return configform.Run(t.Context(),
		configform.Terminal{In: strings.NewReader(script), Out: io.Discard, Accessible: true},
		in)
}

// The requirement in one test: a field starts from the value that is already in
// effect, and accepting it unchanged leaves it unchanged. Every prompt here is
// answered with a bare newline.
func TestAnEmptyAnswerKeepsThePrefilledValue(t *testing.T) {
	got, err := run(t, "\n\n\n", groups())
	if err != nil {
		t.Fatalf("Run = %v", err)
	}
	want := map[string]string{"--gap": "6h", "--log-level": "info", "--vote": "false"}
	for _, f := range got[0].Fields {
		if f.Value != want[f.Title] {
			t.Errorf("%s = %q, want the prefilled %q", f.Title, f.Value, want[f.Title])
		}
	}
}

// Each answer must reach its own field. This is the test that fails if the prompts
// share a buffered reader that reads ahead: the first question would swallow the
// other two answers and they would come back at their prefilled values.
func TestEachAnswerReachesItsOwnField(t *testing.T) {
	// "12h" for the gap, option 4 (error) for the level, "y" for the toggle.
	got, err := run(t, "12h\n4\ny\n", groups())
	if err != nil {
		t.Fatalf("Run = %v", err)
	}
	want := map[string]string{"--gap": "12h", "--log-level": "error", "--vote": "true"}
	for _, f := range got[0].Fields {
		if f.Value != want[f.Title] {
			t.Errorf("%s = %q, want %q", f.Title, f.Value, want[f.Title])
		}
	}
}

// A field's validator is the real parser the setting is read with, so the form must
// reject a bad value and ask again rather than hand it back to be written.
func TestAnInvalidAnswerIsRefusedAndAskedAgain(t *testing.T) {
	in := []configform.Group{{Fields: []configform.Field{{
		Title: "--gap",
		Value: "6h",
		Validate: func(s string) error {
			if s == "nope" {
				return errors.New("not a duration")
			}
			return nil
		},
	}}}}

	var out strings.Builder
	got, err := configform.Run(t.Context(),
		configform.Terminal{In: strings.NewReader("nope\n12h\n"), Out: &out, Accessible: true},
		in)
	if err != nil {
		t.Fatalf("Run = %v", err)
	}
	if v := got[0].Fields[0].Value; v != "12h" {
		t.Errorf("value = %q, want the second, valid answer", v)
	}
	if !strings.Contains(out.String(), "not a duration") {
		t.Errorf("the validator's complaint was never shown:\n%s", out.String())
	}
}

// The caller compares the answers against what it handed in to decide what to write,
// so Run must not write through its argument.
func TestRunDoesNotModifyItsInput(t *testing.T) {
	in := groups()
	if _, err := run(t, "12h\n1\ny\n", in); err != nil {
		t.Fatal(err)
	}
	if v := in[0].Fields[0].Value; v != "6h" {
		t.Errorf("the caller's field was overwritten: %q", v)
	}
}

// Stdin ending early is a user walking away, not a value to write. Every field keeps
// what it started from rather than becoming empty.
func TestEndOfInputLeavesEveryFieldAsItWas(t *testing.T) {
	got, err := run(t, "", groups())
	if err != nil {
		t.Fatalf("Run = %v", err)
	}
	for _, f := range got[0].Fields {
		if f.Value == "" {
			t.Errorf("%s was emptied by end-of-input", f.Title)
		}
	}
}

// A caller may run several forms over one standard input — `moraine config edit` asks
// which settings to change before asking what to change them to. Nothing this package
// wraps the reader in may hold bytes of its own between the two, or the second form
// starts at end-of-input and every answer meant for it is lost.
func TestASecondFormOverTheSameInputStillGetsItsAnswers(t *testing.T) {
	in := strings.NewReader("first\nsecond\n")
	screen := func() configform.Terminal {
		return configform.Terminal{In: in, Out: io.Discard, Accessible: true}
	}
	one := []configform.Group{{Fields: []configform.Field{{Title: "one", Value: "a"}}}}
	two := []configform.Group{{Fields: []configform.Field{{Title: "two", Value: "b"}}}}

	first, err := configform.Run(t.Context(), screen(), one)
	if err != nil {
		t.Fatal(err)
	}
	second, err := configform.Run(t.Context(), screen(), two)
	if err != nil {
		t.Fatal(err)
	}
	if got := first[0].Fields[0].Value; got != "first" {
		t.Errorf("first form = %q, want %q", got, "first")
	}
	if got := second[0].Fields[0].Value; got != "second" {
		t.Errorf("second form = %q, want %q — the first form swallowed its input", got, "second")
	}
}

// A list of choices comes back as the values picked, not the labels shown.
func TestAMultiFieldReturnsTheValuesPicked(t *testing.T) {
	in := []configform.Group{{Fields: []configform.Field{{
		Title: "settings",
		Kind:  configform.Multi,
		Options: []configform.Option{
			{Label: "sort.gap        6h", Value: "sort.gap"},
			{Label: "sort.jobs       0", Value: "sort.jobs"},
		},
	}}}}
	// Pick the second, then 0 to finish.
	got, err := run(t, "2\n0\n", in)
	if err != nil {
		t.Fatalf("Run = %v", err)
	}
	if want := []string{"sort.jobs"}; len(got[0].Fields[0].Values) != 1 || got[0].Fields[0].Values[0] != want[0] {
		t.Errorf("Values = %v, want %v", got[0].Fields[0].Values, want)
	}
}

// Picking nothing is an answer, and must come back as no values rather than as an
// error: it is how a user backs out of the form having changed nothing.
func TestAMultiFieldMayPickNothing(t *testing.T) {
	in := []configform.Group{{Fields: []configform.Field{{
		Title:   "settings",
		Kind:    configform.Multi,
		Options: configform.NewOptions("one", "two"),
	}}}}
	got, err := run(t, "0\n", in)
	if err != nil {
		t.Fatalf("Run = %v", err)
	}
	if len(got[0].Fields[0].Values) != 0 {
		t.Errorf("Values = %v, want none", got[0].Fields[0].Values)
	}
}

// A cancelled context must not come back as an answer. It reports the abort, and the
// caller writes nothing.
func TestACancelledFormWritesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := configform.Run(ctx,
		configform.Terminal{In: strings.NewReader("12h\n"), Out: io.Discard},
		groups())
	if err == nil {
		t.Fatal("Run = nil error for a cancelled form")
	}
}
