package cli_test

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/sgaunet/moraine/internal/cli"
)

func TestSubcommandHelpExitsZero(t *testing.T) {
	for _, sub := range []string{"sort", "clean", "version"} {
		var out bytes.Buffer
		code := cli.Execute("dev", []string{sub, "--help"}, &out, io.Discard)
		if code != 0 {
			t.Errorf("%s --help exit = %d, want 0", sub, code)
		}
		if out.Len() == 0 {
			t.Errorf("%s --help produced no output", sub)
		}
	}
}

func TestSortHelpHasFlagsAndExample(t *testing.T) {
	var out bytes.Buffer
	cli.Execute("dev", []string{"sort", "--help"}, &out, io.Discard)
	s := out.String()
	for _, w := range []string{"Usage:", "Examples:", "moraine sort", "--dest", "--gap", "--sample", "--exiftool"} {
		if !strings.Contains(s, w) {
			t.Errorf("sort help missing %q; got:\n%s", w, s)
		}
	}
}

func TestCleanHelpHasFlagsAndExample(t *testing.T) {
	var out bytes.Buffer
	cli.Execute("dev", []string{"clean", "--help"}, &out, io.Discard)
	s := out.String()
	for _, w := range []string{"Usage:", "Examples:", "moraine clean", "--dest", "--delete"} {
		if !strings.Contains(s, w) {
			t.Errorf("clean help missing %q; got:\n%s", w, s)
		}
	}
}

func TestTopLevelHelpListsAllSubcommands(t *testing.T) {
	var out bytes.Buffer
	cli.Execute("dev", []string{"--help"}, &out, io.Discard)
	s := out.String()
	for _, w := range []string{"sort", "clean", "version"} {
		if !strings.Contains(s, w) {
			t.Errorf("top-level help missing subcommand %q; got:\n%s", w, s)
		}
	}
}
