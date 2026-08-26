package cli_test

import (
	"bytes"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sgaunet/moraine/internal/cli"
	"github.com/sgaunet/moraine/internal/exiftooltest"
	"github.com/sgaunet/moraine/internal/ui"
)

// sortStderr runs a one-event sort and returns what reached stderr.
func sortStderr(t *testing.T, extra ...string) string {
	t.Helper()
	src, dest := t.TempDir(), t.TempDir()
	writePNG(t, filepath.Join(src, "a.png"))
	exifPath, err := exiftooltest.Stub(t.TempDir(), exiftooltest.Options{})
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{"sort", "--exiftool", exifPath, "-s", "0", "-d", dest}, extra...)
	var errs bytes.Buffer
	if code := cli.Execute("dev", append(args, src), io.Discard, &errs); code != 0 {
		t.Fatalf("exit = %d, want 0; stderr:\n%s", code, errs.String())
	}
	return errs.String()
}

// The bullet rendering drops the messages a bar already accounts for. That filter
// matches on the message, so it is only correct while the pipeline still emits those
// messages: this drives a real run through the plain rendering and fails if one of
// them is gone, which is what stops a rename from turning the filter into a no-op.
func TestPlainRenderingStillEmitsWhatTheBarsReplace(t *testing.T) {
	narrated := ui.PhaseNarration()
	if len(narrated) == 0 {
		t.Skip("no phase narration is filtered")
	}
	stderr := sortStderr(t, "--progress=never")
	for _, msg := range narrated {
		if !strings.Contains(stderr, "msg="+msg) {
			t.Errorf("the pipeline no longer logs %q, so the bullet renderer filters "+
				"nothing — drop it from phaseNarration or fix the name:\n%s", msg, stderr)
		}
	}
}

// And the same run drawn as bullets does not repeat them.
func TestBulletRenderingDropsWhatTheBarsReplace(t *testing.T) {
	narrated := ui.PhaseNarration()
	if len(narrated) == 0 {
		t.Skip("no phase narration is filtered")
	}
	drawn := bulletMessages(sortStderr(t, "--progress=always"))
	for _, msg := range narrated {
		if slices.Contains(drawn, msg) {
			t.Errorf("%q should not be drawn beside the bar that says it, got lines:\n%s",
				msg, strings.Join(drawn, "\n"))
		}
	}
}

// bulletMessages returns the first word of every bullet line, which is the message the
// pipeline logged. Compared word by word rather than by substring: "group" is also a
// prefix of the "groups=1" attribute on the cluster line, which is not the same thing.
func bulletMessages(stderr string) []string {
	var out []string
	for line := range strings.SplitSeq(stripANSI(stderr), "\n") {
		line = strings.TrimSpace(line)
		// Every drawn line starts with a bullet glyph and a space.
		_, rest, found := strings.Cut(line, " ")
		if !found {
			continue
		}
		if word, _, _ := strings.Cut(strings.TrimSpace(rest), " "); word != "" {
			out = append(out, word)
		}
	}
	return out
}

// stripANSI removes the colouring, so a test compares words rather than escapes.
func stripANSI(s string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, "\x1b[")
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		s = s[i+2:]
		if j := strings.IndexByte(s, 'm'); j >= 0 {
			s = s[j+1:]
		}
	}
}
