// Package configform asks a user for configuration values through an interactive
// terminal form, built on github.com/charmbracelet/huh.
//
// It knows nothing about moraine's settings, its flags, or its defaults: the caller
// hands it fully-resolved fields — a title, a help line, a kind, and the value to
// start from — and gets the same fields back holding whatever the user chose. That
// is what keeps the terminal out of internal/cli and lets this package be tested
// without one (Constitution Principle III).
//
// Two rules the form obeys, both from Constitution Principle V:
//
//   - It draws on the writer it is given, which the caller points at stderr. Stdout
//     carries data only, and an interactive form is not data.
//   - Accessible mode replaces the full-screen interface with plain line-by-line
//     prompts. It is what a user who needs a screen reader turns on, and it is also
//     the only mode that works without a terminal, which is what makes this package
//     testable from a strings.Reader.
package configform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/charmbracelet/huh"
)

// ErrAborted reports that the user left the form without submitting it. Nothing they
// typed should be written.
var ErrAborted = errors.New("form aborted")

// Kind selects how one field is presented.
type Kind int

const (
	// Text asks for a free-form value.
	Text Kind = iota
	// Choice offers a closed list of values, of which one is the answer.
	Choice
	// Toggle asks a yes/no question. Its Value is "true" or "false".
	Toggle
	// Multi offers a list of which any number may be picked. Its answer is Values.
	Multi
)

// Option is one entry of a Choice or Multi field: the text a reader sees, and the
// value that comes back when it is picked. They differ whenever the value is a key
// and the label is something a human would rather read.
type Option struct {
	Label string
	Value string
}

// NewOptions builds options whose label and value are the same word.
func NewOptions(values ...string) []Option {
	out := make([]Option, len(values))
	for i, v := range values {
		out[i] = Option{Label: v, Value: v}
	}
	return out
}

// Field is one question. Value (or Values, for a Multi) is both the answer and, on
// the way in, what the question starts from — the current setting where there is one,
// the default where there is not.
type Field struct {
	Title    string
	Help     string
	Kind     Kind
	Options  []Option
	Value    string
	Values   []string
	Validate func(string) error
}

// Group is one page of the form.
type Group struct {
	Title       string
	Description string
	Fields      []Field
}

// Terminal is where the form draws and reads. Accessible swaps the full-screen
// interface for line-by-line prompts.
type Terminal struct {
	In         io.Reader
	Out        io.Writer
	Accessible bool
}

// Run presents the groups and returns them holding the user's answers. The input is
// never modified: the returned groups are a copy, so a caller can compare the two to
// see what actually changed.
//
// A user who quits the form gets ErrAborted and no answers.
func Run(ctx context.Context, term Terminal, groups []Group) ([]Group, error) {
	answers := clone(groups)

	// huh binds each field to a variable it writes through. Strings back everything
	// except a toggle, which needs a real bool; both are converted back into the
	// field's own string form once the form returns.
	texts := make([][]string, len(answers))
	flags := make([][]bool, len(answers))
	lists := make([][][]string, len(answers))
	formGroups := make([]*huh.Group, 0, len(answers))

	for g := range answers {
		texts[g] = make([]string, len(answers[g].Fields))
		flags[g] = make([]bool, len(answers[g].Fields))
		lists[g] = make([][]string, len(answers[g].Fields))
		fields := make([]huh.Field, 0, len(answers[g].Fields))
		for f := range answers[g].Fields {
			fields = append(fields, bind(answers[g].Fields[f], &texts[g][f], &flags[g][f], &lists[g][f]))
		}
		group := huh.NewGroup(fields...).Title(answers[g].Title)
		if answers[g].Description != "" {
			group = group.Description(answers[g].Description)
		}
		formGroups = append(formGroups, group)
	}

	input := term.In
	if term.Accessible {
		input = newLineReader(input)
	}
	form := huh.NewForm(formGroups...).
		WithInput(input).
		WithOutput(term.Out).
		WithAccessible(term.Accessible)

	if err := form.RunWithContext(ctx); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil, ErrAborted
		}
		return nil, fmt.Errorf("reading the form: %w", err)
	}

	for g := range answers {
		for f := range answers[g].Fields {
			switch answers[g].Fields[f].Kind {
			case Toggle:
				answers[g].Fields[f].Value = strconv.FormatBool(flags[g][f])
			case Multi:
				answers[g].Fields[f].Values = lists[g][f]
			case Text, Choice:
				answers[g].Fields[f].Value = texts[g][f]
			}
		}
	}
	return answers, nil
}

// bind turns one field into the huh field that asks for it, pointed at the variable
// that will hold the answer.
func bind(f Field, text *string, flag *bool, list *[]string) huh.Field {
	switch f.Kind {
	case Multi:
		*list = append([]string(nil), f.Values...)
		return huh.NewMultiSelect[string]().
			Title(f.Title).
			Description(f.Help).
			Options(options(f.Options, f.Values...)...).
			Filterable(true).
			Value(list)

	case Toggle:
		*flag, _ = strconv.ParseBool(f.Value)
		return huh.NewConfirm().
			Title(f.Title).
			Description(f.Help).
			Affirmative("yes").
			Negative("no").
			Value(flag)

	case Choice:
		*text = f.Value
		return huh.NewSelect[string]().
			Title(f.Title).
			Description(f.Help).
			Options(options(f.Options)...).
			Value(text)

	case Text:
		fallthrough
	default:
		*text = f.Value
		input := huh.NewInput().
			Title(f.Title).
			Description(f.Help).
			Value(text)
		if f.Validate != nil {
			input = input.Validate(f.Validate)
		}
		return input
	}
}

// lineReader hands out one byte per Read, and buffers nothing of its own.
//
// It exists because huh's accessible prompts build a fresh bufio.Scanner for every
// field, all of them over the same reader. A scanner reads ahead by up to 64 KiB, so
// the first question would swallow the answers to all the rest and every later prompt
// would see EOF. Reading a byte at a time makes each scanner stop at the newline it
// was after and leave the remainder where it is.
//
// Holding no buffer is the other half of that, and just as load-bearing: a caller may
// run several forms in sequence over one standard input — `moraine config edit` asks
// which settings to change before asking what to change them to — and anything this
// wrapper read ahead would be lost when the form that owned it returned.
//
// It costs a read per byte, which is nothing next to a human typing, and it is only
// used in accessible mode: the full-screen interface reads raw key events instead.
type lineReader struct{ source io.Reader }

func newLineReader(r io.Reader) *lineReader {
	return &lineReader{source: r}
}

func (l *lineReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return l.source.Read(p[:1]) // errors pass through: a Reader must report io.EOF as itself
}

// options renders the field's choices as huh options, marking any that start out
// picked.
func options(from []Option, selected ...string) []huh.Option[string] {
	chosen := make(map[string]bool, len(selected))
	for _, v := range selected {
		chosen[v] = true
	}
	out := make([]huh.Option[string], 0, len(from))
	for _, o := range from {
		out = append(out, huh.NewOption(o.Label, o.Value).Selected(chosen[o.Value]))
	}
	return out
}

// clone copies the groups so that Run never writes through its argument: the caller
// keeps the values it started from, which is what it compares the answers against.
func clone(groups []Group) []Group {
	out := make([]Group, len(groups))
	for i, g := range groups {
		out[i] = g
		out[i].Fields = make([]Field, len(g.Fields))
		copy(out[i].Fields, g.Fields)
		for f := range out[i].Fields {
			out[i].Fields[f].Values = append([]string(nil), g.Fields[f].Values...)
		}
	}
	return out
}
