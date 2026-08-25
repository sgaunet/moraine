package configfile_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/sgaunet/moraine/internal/configfile"
)

// intNode and strNode build the value nodes a caller hands to Set.
func intNode(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: v}
}

func strNode(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
}

// open reads contents from a fresh temp file and returns the Document plus its path.
func open(t *testing.T, contents string) (*configfile.Document, string) {
	t.Helper()
	path := write(t, contents)
	d, err := configfile.Open(path)
	if err != nil {
		t.Fatalf("Open = %v", err)
	}
	return d, path
}

// The reason this package edits a node tree instead of decoding into File and
// marshalling back: a configuration file is hand-maintained, and comments are the
// part of it moraine did not write. This asserts the whole file byte for byte —
// a decode-and-re-marshal implementation loses all three comments and fails here.
//
// The input is already in the layout the encoder emits (two-space indent, no blank
// line between plain keys), which is what makes an exact comparison meaningful; see
// TestWritingTwiceChangesNothingFurther for the normalisation itself.
func TestSetPreservesCommentsAndEveryUntouchedKey(t *testing.T) {
	const before = `# a configuration file with opinions in it

log_level: warn # noisy at info
output: json
sort:
  # a long day out is still one event
  gap: 6h
  themes: [mountain, cook]
`
	const after = `# a configuration file with opinions in it

log_level: warn # noisy at info
output: json
sort:
  # a long day out is still one event
  gap: 6h
  themes: [mountain, cook]
  jobs: 4
`
	d, _ := open(t, before)
	if err := d.Set([]string{"sort", "jobs"}, intNode("4")); err != nil {
		t.Fatalf("Set = %v", err)
	}
	got, err := d.Bytes()
	if err != nil {
		t.Fatalf("Bytes = %v", err)
	}
	if string(got) != after {
		t.Errorf("the file was not preserved.\n--- got ---\n%s\n--- want ---\n%s", got, after)
	}
}

// A comment written at the end of the line belongs to the *value* node, so replacing
// that node — the obvious way to implement Set — is exactly how such a comment gets
// lost. This pins the in-place rewrite that avoids it.
func TestSetKeepsTheCommentOnTheLineItChanges(t *testing.T) {
	d, _ := open(t, "sort:\n  gap: 6h # a long day out is still one event\n")
	if err := d.Set([]string{"sort", "gap"}, strNode("12h")); err != nil {
		t.Fatalf("Set = %v", err)
	}
	got, err := d.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	want := "sort:\n  gap: 12h # a long day out is still one event\n"
	if string(got) != want {
		t.Errorf("Bytes =\n%s\nwant\n%s", got, want)
	}
}

// Setting a value before there is any configuration file must work: that is the
// first thing a new user does. The created file explains itself.
func TestSetIntoAMissingFileCreatesOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "moraine.yaml")
	d, err := configfile.Open(path)
	if err != nil {
		t.Fatalf("Open of a missing file = %v, want no error", err)
	}
	if d.Existed() {
		t.Error("Existed = true for a file that is not there")
	}
	if err := d.Set([]string{"sort", "gap"}, strNode("8h")); err != nil {
		t.Fatal(err)
	}
	if err := d.Save(); err != nil {
		t.Fatalf("Save = %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), "# moraine configuration") {
		t.Errorf("a created file should open with a header comment, got:\n%s", got)
	}
	if !strings.Contains(string(got), "gap: 8h") {
		t.Errorf("the setting is missing from:\n%s", got)
	}

	// It must still decode, strictly, as the configuration it claims to be.
	f, err := configfile.Parse(got)
	if err != nil {
		t.Fatalf("the file moraine wrote does not parse: %v", err)
	}
	if s := f.SortSection(); s.Gap == nil || s.Gap.String() != "8h0m0s" {
		t.Errorf("gap round-tripped as %v", s.Gap)
	}
}

// An empty file, and one holding only comments, decode to no YAML node at all.
// Both are valid configurations that set nothing, so Set must start from them
// rather than refuse.
func TestSetIntoAFileWithNoSettings(t *testing.T) {
	for name, contents := range map[string]string{
		"empty":        "",
		"comment only": "# notes to self\n",
	} {
		t.Run(name, func(t *testing.T) {
			d, _ := open(t, contents)
			if !d.Existed() {
				t.Error("Existed = false for a file that is on disk")
			}
			if err := d.Set([]string{"output"}, strNode("json")); err != nil {
				t.Fatal(err)
			}
			got, err := d.Bytes()
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(got), "output: json") {
				t.Errorf("Bytes = %q", got)
			}
		})
	}
}

// A section the file does not have yet is created, separated from what precedes it
// so the result stays readable.
func TestSetCreatesAMissingSection(t *testing.T) {
	d, _ := open(t, "output: json\n")
	if err := d.Set([]string{"clean", "dest"}, strNode("/photos")); err != nil {
		t.Fatal(err)
	}
	got, err := d.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	want := "output: json\n\nclean:\n  dest: /photos\n"
	if string(got) != want {
		t.Errorf("Bytes =\n%q\nwant\n%q", got, want)
	}
}

// Unsetting the last setting of a section must take the section with it: leaving
// "sort: {}" behind would be debris, and a file moraine wrote should not need
// tidying by hand.
func TestUnsetPrunesASectionItEmpties(t *testing.T) {
	d, _ := open(t, "output: json\nsort:\n  gap: 6h\n")
	if !d.Unset([]string{"sort", "gap"}) {
		t.Fatal("Unset = false, want true for a setting that is there")
	}
	got, err := d.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "output: json\n" {
		t.Errorf("Bytes = %q, want the emptied section pruned", got)
	}
}

// A section with settings left in it is kept.
func TestUnsetKeepsASectionThatStillHasSettings(t *testing.T) {
	d, _ := open(t, "sort:\n  gap: 6h\n  jobs: 4\n")
	if !d.Unset([]string{"sort", "gap"}) {
		t.Fatal("Unset = false")
	}
	got, err := d.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "sort:\n  jobs: 4\n" {
		t.Errorf("Bytes = %q", got)
	}
}

// Unsetting what is not there is not an error, it is just nothing to do — the
// caller reports "not set" rather than failing.
func TestUnsetReportsASettingThatIsNotThere(t *testing.T) {
	d, _ := open(t, "sort:\n  gap: 6h\n")
	for _, keys := range [][]string{{"sort", "jobs"}, {"clean", "dest"}, {"output"}} {
		if d.Unset(keys) {
			t.Errorf("Unset(%v) = true, want false", keys)
		}
	}
}

// Unsetting every setting must not leave "{}" in the file. A file with no keys is
// one that configures nothing, which is exactly what it should look like.
func TestUnsettingEverythingLeavesNoMapping(t *testing.T) {
	d, _ := open(t, "output: json\n")
	if !d.Unset([]string{"output"}) {
		t.Fatal("Unset = false")
	}
	got, err := d.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "{}") {
		t.Errorf("Bytes = %q, want no empty mapping", got)
	}
	if _, err := configfile.Parse(got); err != nil {
		t.Errorf("the emptied file does not parse: %v", err)
	}
}

// The first write normalises indentation, because yaml.v3 re-emits the tree with its
// own layout. What matters is that it settles: a file moraine has written once is
// never rewritten again just for looking at it.
func TestWritingTwiceChangesNothingFurther(t *testing.T) {
	d, path := open(t, "sort:\n    gap: 6h\n    jobs: 4\n") // four-space indent
	if err := d.Save(); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != "sort:\n  gap: 6h\n  jobs: 4\n" {
		t.Errorf("first write = %q, want two-space indent", first)
	}

	again, err := configfile.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := again.Save(); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(second) != string(first) {
		t.Errorf("a second write changed the file:\n%q\nvs\n%q", second, first)
	}
}

// A file whose top level is not a mapping is someone else's YAML. Rewriting it would
// be worse than refusing, so the error names what was found.
func TestOpenRefusesAFileThatIsNotSettings(t *testing.T) {
	for name, contents := range map[string]string{
		"a list":   "- one\n- two\n",
		"a scalar": "just a string\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := write(t, contents)
			_, err := configfile.Open(path)
			if err == nil {
				t.Fatal("Open = nil error, want a refusal")
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("the error should name the file, got %v", err)
			}
		})
	}
}

// Setting into a key that holds a value rather than a section is a mistake worth
// reporting, not something to overwrite.
func TestSetRefusesToTreatAValueAsASection(t *testing.T) {
	d, _ := open(t, "sort: nonsense\n")
	err := d.Set([]string{"sort", "gap"}, strNode("6h"))
	if err == nil {
		t.Fatal("Set = nil error, want a refusal")
	}
	if !strings.Contains(err.Error(), "sort") {
		t.Errorf("the error should name the key, got %v", err)
	}
}

// A configuration file may hold a destination path and is written into the user's
// home: it is published atomically and readable only by them, and a failed write
// must not leave a staging file behind.
func TestSaveIsAtomicAndPrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "moraine.yaml") // the directory does not exist yet
	d, err := configfile.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Set([]string{"output"}, strNode("json")); err != nil {
		t.Fatal(err)
	}
	if err := d.Save(); err != nil {
		t.Fatalf("Save = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("a staging file was left behind: %s", e.Name())
		}
	}
}

// Target answers the same search order Load reads, so `config set` writes to the
// file the next run will read.
func TestTargetFollowsTheSearchOrder(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := configfile.Target("")
	if err != nil {
		t.Fatalf("Target = %v", err)
	}
	if want := filepath.Join(home, ".config", "moraine.yaml"); got.Path != want {
		t.Errorf("Target = %q, want %q", got.Path, want)
	}
	if got.Source != configfile.SourceHome {
		t.Errorf("Source = %q, want %q", got.Source, configfile.SourceHome)
	}

	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	got, _ = configfile.Target("")
	if got.Path != filepath.Join(xdg, "moraine.yaml") || got.Source != configfile.SourceXDG {
		t.Errorf("XDG_CONFIG_HOME ignored: Target = %+v", got)
	}

	got, _ = configfile.Target("/tmp/named.yaml")
	if got.Path != "/tmp/named.yaml" || got.Source != configfile.SourceFlag {
		t.Errorf("an explicit path must win: Target = %+v", got)
	}
}

// MORAINE_CONFIG= means "no configuration file". A run treats that as silence, but a
// write has nowhere to go, so it must say so rather than invent a path.
func TestTargetRefusesWhenTheFileIsTurnedOff(t *testing.T) {
	isolate(t)
	t.Setenv(configfile.EnvVar, "")
	_, err := configfile.Target("")
	if err == nil {
		t.Fatal("Target = nil error, want a refusal")
	}
	if !strings.Contains(err.Error(), configfile.EnvVar) {
		t.Errorf("the error should name %s, got %v", configfile.EnvVar, err)
	}
}

// Parse applies the same strictness as reading a file, which is what makes it usable
// as the check before a write.
func TestParseIsStrict(t *testing.T) {
	if _, err := configfile.Parse([]byte("sort:\n  gpa: 6h\n")); err == nil {
		t.Error("Parse accepted an unknown key, want an error")
	}
	if f, err := configfile.Parse(nil); err != nil || f == nil {
		t.Errorf("Parse(nil) = (%v, %v), want an empty file and no error", f, err)
	}
}
