package cli_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sgaunet/moraine/internal/cli"
	"github.com/sgaunet/moraine/internal/exiftooltest"
)

func TestSortOrganizesPNG(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	writePNG(t, filepath.Join(src, "a.png"))

	// A working stub satisfies the unconditional exiftool check even with the model off.
	exifPath, err := exiftooltest.Stub(t.TempDir(), exiftooltest.Options{})
	if err != nil {
		t.Fatal(err)
	}

	code := cli.Execute("dev", []string{
		"sort", "--exiftool", exifPath, "--sample", "0", "--dest", dest, src,
	}, io.Discard, io.Discard)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}

	// The PNG should have been organized somewhere under dest (behavior parity).
	var copied bool
	_ = filepath.Walk(dest, func(_ string, info os.FileInfo, _ error) error {
		if info != nil && !info.IsDir() && strings.HasSuffix(info.Name(), ".png") {
			copied = true
		}
		return nil
	})
	if !copied {
		t.Error("expected the PNG to be organized under dest")
	}
}

func TestSortShortFlags(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	writePNG(t, filepath.Join(src, "a.png"))
	exifPath, err := exiftooltest.Stub(t.TempDir(), exiftooltest.Options{})
	if err != nil {
		t.Fatal(err)
	}
	// -s (sample) and -d (dest) shorthands must work like their long forms.
	code := cli.Execute("dev", []string{"sort", "--exiftool", exifPath, "-s", "0", "-d", dest, src}, io.Discard, io.Discard)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
}

func TestSortMissingExiftoolFailsFast(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	writePNG(t, filepath.Join(src, "a.png"))

	var stderr bytes.Buffer
	code := cli.Execute("dev", []string{
		"sort", "--exiftool", filepath.Join(t.TempDir(), "no-such-exiftool"),
		"--sample", "0", "--dest", dest, src,
	}, io.Discard, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (runtime); stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "exiftool") {
		t.Errorf("stderr should mention exiftool; got: %s", stderr.String())
	}
	// Nothing must have been scanned or copied.
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("destination not empty after fail-fast: %v", entries)
	}
}

func TestSortMissingSourceIsRuntime(t *testing.T) {
	// Validate runs before the exiftool preflight, so a missing source is exit 1.
	code := cli.Execute("dev", []string{
		"sort", "--sample", "0", "--dest", t.TempDir(), filepath.Join(t.TempDir(), "nope"),
	}, io.Discard, io.Discard)
	if code != 1 {
		t.Fatalf("missing source exit = %d, want 1 (runtime)", code)
	}
}

func TestSortInvalidValuesAreUsage(t *testing.T) {
	tmp := t.TempDir()
	tests := []struct {
		name string
		args []string
	}{
		{"bad gap", []string{"sort", "--gap", "nope", tmp}},
		{"bad themes slug", []string{"sort", "--themes", "Bad Theme", tmp}},
		{"negative sample", []string{"sort", "--sample", "-1", tmp}},
		{"zero mountain altitude", []string{"sort", "--mountain-altitude", "0", tmp}},
		{"negative mountain altitude", []string{"sort", "--mountain-altitude", "-100", tmp}},
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

// TestSortDeciderUsageErrors: every bad decider setting is refused before anything
// is scanned, including jev with no key in the environment.
func TestSortDeciderUsageErrors(t *testing.T) {
	src := t.TempDir()
	writePNG(t, filepath.Join(src, "a.png"))
	t.Setenv("TYPESAFE_API_KEY", "")
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"unknown decider", []string{"--decider", "bogus"}, "--decider"},
		{"jev without a key", []string{"--decider", "jev"}, "TYPESAFE_API_KEY"},
		{"confidence out of range", []string{"--decider-min-confidence", "1.5"}, "--decider-min-confidence"},
		{"relative url", []string{"--decider", "laya", "--decider-url", "localhost:8000"}, "--decider-url"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			args := append([]string{"sort", "--dest", t.TempDir()}, tc.args...)
			code := cli.Execute("dev", append(args, src), io.Discard, &stderr)
			if code != 2 {
				t.Fatalf("exit = %d, want 2; stderr: %s", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("stderr does not name %q: %s", tc.want, stderr.String())
			}
			if strings.Contains(stderr.String(), "scan") {
				t.Errorf("a usage error must come before the scan: %s", stderr.String())
			}
		})
	}
}

// TestSortNeverPrintsTheDeciderKey runs a verbose sort with a key in the
// environment and checks neither stream carries it.
func TestSortNeverPrintsTheDeciderKey(t *testing.T) {
	const key = "k3y-xyz-never-shown"
	t.Setenv("LAYA_API_KEY", key)
	src := t.TempDir()
	writePNG(t, filepath.Join(src, "a.png"))
	exifPath, err := exiftooltest.Stub(t.TempDir(), exiftooltest.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := cli.Execute("dev", []string{
		"sort", "-v", "--progress=never", "--output=json", "--exiftool", exifPath,
		"--ollama-url", "http://127.0.0.1:9", "--decider", "laya", "--decider-url", "http://127.0.0.1:9",
		"--dest", t.TempDir(), src,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), key) {
		t.Error("the API key was printed")
	}
}

func TestSortThemeDescriptions(t *testing.T) {
	tests := []struct {
		name string
		file string
		args []string
		want int
	}{
		{"one description", "", []string{"--theme-description", "cook=food, meals, market stalls"}, 0},
		// The second occurrence is collected too, so its bad slug is what fails.
		{"repeated flags are all read", "", []string{"--theme-description", "cook=food", "--theme-description", "nope=x"}, 2},
		{"an unconfigured theme", "", []string{"--theme-description", "nope=x"}, 2},
		{"the file is read", "sort:\n  theme_description:\n    nope: x\n", nil, 2},
		// Any flag replaces the file's whole mapping, so its bad entry is gone.
		{"a flag replaces the whole file mapping", "sort:\n  theme_description:\n    nope: x\n",
			[]string{"--theme-description", "cook=food"}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			extra := tc.args
			if tc.file != "" {
				extra = append([]string{"--config", writeConfig(t, tc.file)}, extra...)
			}
			args, _ := sortFixture(t, extra...)
			var stderr bytes.Buffer
			if code := cli.Execute("dev", args, io.Discard, &stderr); code != tc.want {
				t.Errorf("exit = %d, want %d; stderr: %s", code, tc.want, stderr.String())
			}
		})
	}
}
