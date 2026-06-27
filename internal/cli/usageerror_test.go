package cli_test

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/sgaunet/moraine/internal/cli"
)

func TestUsageErrorsExitTwo(t *testing.T) {
	tmp := t.TempDir()
	tests := []struct {
		name string
		args []string
	}{
		{"unknown command", []string{"bogus"}},
		{"unknown flag", []string{"sort", "--nope", tmp}},
		{"missing positional", []string{"sort"}},
		{"too many positionals", []string{"sort", tmp, tmp}},
		{"invalid flag value", []string{"sort", "--sample", "-1", tmp}},
		{"legacy rootless form", []string{tmp}},                               // moraine <dir> (no subcommand) — FR-015
		{"legacy single-dash long flag", []string{"sort", "-dest", "X", tmp}}, // FR-015
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code := cli.Execute("dev", tc.args, io.Discard, io.Discard)
			if code != 2 {
				t.Errorf("%s: exit = %d, want 2 (usage)", tc.name, code)
			}
		})
	}
}

func TestUnknownCommandNamesOffender(t *testing.T) {
	var stderr bytes.Buffer
	cli.Execute("dev", []string{"bogus"}, io.Discard, &stderr)
	s := stderr.String()
	if !strings.Contains(s, "bogus") {
		t.Errorf("error should name the unknown command; got: %s", s)
	}
	if !strings.Contains(s, "--help") {
		t.Errorf("error should point to help; got: %s", s)
	}
}

func TestUnknownFlagNamesOffender(t *testing.T) {
	var stderr bytes.Buffer
	cli.Execute("dev", []string{"sort", "--nope", t.TempDir()}, io.Discard, &stderr)
	if !strings.Contains(stderr.String(), "nope") {
		t.Errorf("error should name the unknown flag; got: %s", stderr.String())
	}
}
