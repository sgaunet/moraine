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
	// The first line is the stable identity; the build detail follows it.
	if got := firstLine(out.String()); got != "moraine 1.2.3" {
		t.Errorf("version first line = %q, want %q", got, "moraine 1.2.3")
	}
	// go and platform are always knowable; the vcs stamp may be absent (a test
	// binary, or a build made outside a repository), so it is not asserted.
	for _, want := range []string{"go=", "platform="} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("version output missing %q\n---\n%s", want, out.String())
		}
	}
}

// TestVersionFlagIsTheTerseForm holds the two spellings together: --version prints
// exactly the subcommand's first line, so neither can drift into a different name.
func TestVersionFlagIsTheTerseForm(t *testing.T) {
	var sub, flag bytes.Buffer
	if code := cli.Execute("1.2.3", []string{"version"}, &sub, io.Discard); code != 0 {
		t.Fatalf("version subcommand exit = %d", code)
	}
	if code := cli.Execute("1.2.3", []string{"--version"}, &flag, io.Discard); code != 0 {
		t.Fatalf("--version flag exit = %d", code)
	}
	if strings.TrimSpace(flag.String()) != firstLine(sub.String()) {
		t.Errorf("--version %q is not the subcommand's first line %q",
			strings.TrimSpace(flag.String()), firstLine(sub.String()))
	}
}

// firstLine returns s up to the first newline, trimmed.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
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
