package cli_test

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/sgaunet/moraine/internal/cli"
)

func TestVersionSubcommand(t *testing.T) {
	var out bytes.Buffer
	code := cli.Execute("1.2.3", []string{"version"}, &out, io.Discard)
	if code != 0 {
		t.Fatalf("version exit = %d, want 0", code)
	}
	if strings.TrimSpace(out.String()) != "moraine 1.2.3" {
		t.Errorf("version output = %q, want %q", strings.TrimSpace(out.String()), "moraine 1.2.3")
	}
}

func TestVersionFlagMatchesSubcommand(t *testing.T) {
	var sub, flag bytes.Buffer
	if code := cli.Execute("1.2.3", []string{"version"}, &sub, io.Discard); code != 0 {
		t.Fatalf("version subcommand exit = %d", code)
	}
	if code := cli.Execute("1.2.3", []string{"--version"}, &flag, io.Discard); code != 0 {
		t.Fatalf("--version flag exit = %d", code)
	}
	if sub.String() != flag.String() {
		t.Errorf("version subcommand %q != --version %q", sub.String(), flag.String())
	}
}

func TestVersionNeedsNoSourceOrTools(t *testing.T) {
	// version must succeed with no source argument and touches no filesystem or
	// external tools (FR-013/SC-004/SC-006).
	var out bytes.Buffer
	code := cli.Execute("9.9.9", []string{"version"}, &out, io.Discard)
	if code != 0 {
		t.Fatalf("version exit = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "9.9.9") {
		t.Errorf("version output missing the version; got %q", out.String())
	}
}

func TestVersionRejectsArgs(t *testing.T) {
	// `version` takes no positional arguments.
	code := cli.Execute("dev", []string{"version", "extra"}, io.Discard, io.Discard)
	if code != 2 {
		t.Errorf("version with an extra arg exit = %d, want 2 (usage)", code)
	}
}
