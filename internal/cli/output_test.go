package cli_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sgaunet/moraine/internal/cli"
)

// sortFixture builds a source holding one photo plus one companion, and returns the
// arguments that sort it into a fresh destination.
func sortFixture(t *testing.T, extra ...string) (args []string, dest string) {
	t.Helper()
	src, dest := t.TempDir(), t.TempDir()
	writePNG(t, filepath.Join(src, "a.png"))
	writeCLIFile(t, filepath.Join(src, "a.xmp"), "base")
	args = append([]string{"sort", "--sample", "0", "--exiftool", stubExif(t), "--dest", dest}, extra...)
	return append(args, src), dest
}

// TestStdoutCarriesDataOnly is the Constitution Principle V regression test: stdout
// is the run's result and nothing else. Logs used to be written there, which corrupts
// every downstream consumer of a pipe.
func TestStdoutCarriesDataOnly(t *testing.T) {
	t.Run("sort", func(t *testing.T) {
		args, _ := sortFixture(t, "--verbose") // --verbose: as chatty as moraine gets
		var out, errb bytes.Buffer
		if code := cli.Execute("dev", args, &out, &errb); code != 0 {
			t.Fatalf("exit = %d; stderr=%s", code, errb.String())
		}
		assertNoLogs(t, out.String())
		if !strings.Contains(errb.String(), "level=") {
			t.Errorf("the logs must land on stderr; got:\n%s", errb.String())
		}
	})

	t.Run("clean", func(t *testing.T) {
		src, dst := t.TempDir(), t.TempDir()
		writeCLIFile(t, filepath.Join(src, "IMG.jpg"), "PIC")
		writeCLIFile(t, filepath.Join(dst, "IMG.jpg"), "PIC")

		var out, errb bytes.Buffer
		code := cli.Execute("dev", []string{"clean", "--dest", dst, src}, &out, &errb)
		if code != 0 {
			t.Fatalf("exit = %d; stderr=%s", code, errb.String())
		}
		assertNoLogs(t, out.String())
		// clean's per-file plan is its product, so it stays visible by default —
		// on stderr, where it cannot corrupt a redirected stdout.
		if !strings.Contains(errb.String(), "decision=would-delete") {
			t.Errorf("the plan must still be reported on stderr; got:\n%s", errb.String())
		}
	})
}

// assertNoLogs fails if s carries anything that looks like a slog line.
func assertNoLogs(t *testing.T, s string) {
	t.Helper()
	for _, marker := range []string{"level=", "time=", "msg="} {
		if strings.Contains(s, marker) {
			t.Errorf("stdout must carry data only, found log marker %q in:\n%s", marker, s)
		}
	}
}

func TestSortTextSummaryIsOneLine(t *testing.T) {
	args, _ := sortFixture(t)
	var out bytes.Buffer
	if code := cli.Execute("dev", args, &out, io.Discard); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("want a single summary line, got %d:\n%s", len(lines), out.String())
	}
	for _, want := range []string{
		"groups=1", "copied=1", "skipped=0", "renamed=0", "errors=0",
		"companions_copied=1", "dry_run=false", "interrupted=false",
	} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("summary missing %q; got: %s", want, lines[0])
		}
	}
}

func TestSortJSONOutput(t *testing.T) {
	args, dest := sortFixture(t, "--output=json")
	var out bytes.Buffer
	if code := cli.Execute("dev", args, &out, io.Discard); code != 0 {
		t.Fatalf("exit = %d", code)
	}

	var doc struct {
		Command     string `json:"command"`
		Source      string `json:"source"`
		Dest        string `json:"dest"`
		DryRun      bool   `json:"dry_run"`
		Interrupted bool   `json:"interrupted"`
		Results     []struct {
			Source    string `json:"source"`
			Dest      string `json:"dest"`
			Theme     string `json:"theme"`
			Date      string `json:"date"`
			Action    string `json:"action"`
			Companion bool   `json:"companion"`
			Of        string `json:"of"`
		} `json:"results"`
		Summary struct {
			Groups           int `json:"groups"`
			Copied           int `json:"copied"`
			CompanionsCopied int `json:"companions_copied"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, out.String())
	}

	if doc.Command != "sort" || doc.Dest != dest || doc.DryRun || doc.Interrupted {
		t.Errorf("document header = %+v", doc)
	}
	if doc.Summary.Groups != 1 || doc.Summary.Copied != 1 || doc.Summary.CompanionsCopied != 1 {
		t.Errorf("summary = %+v", doc.Summary)
	}
	// One record per placed file: the photo and its companion.
	if len(doc.Results) != 2 {
		t.Fatalf("want 2 records, got %d: %+v", len(doc.Results), doc.Results)
	}
	var companions int
	for _, r := range doc.Results {
		if r.Action != "copied" || r.Dest == "" || r.Theme == "" || r.Date == "" {
			t.Errorf("incomplete record: %+v", r)
		}
		if r.Companion {
			companions++
			if r.Of == "" {
				t.Error("a companion record must name its photo")
			}
		}
	}
	if companions != 1 {
		t.Errorf("companion records = %d, want 1", companions)
	}
}

func TestCleanJSONOutput(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	writeCLIFile(t, filepath.Join(src, "IMG.jpg"), "PIC")
	writeCLIFile(t, filepath.Join(dst, "IMG.jpg"), "PIC")

	var out bytes.Buffer
	code := cli.Execute("dev", []string{"clean", "--output=json", "--dest", dst, src}, &out, io.Discard)
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}

	var doc struct {
		Command string `json:"command"`
		Delete  bool   `json:"delete"`
		Results []struct {
			Path     string `json:"path"`
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		} `json:"results"`
		Summary struct {
			WouldDelete int `json:"would_delete"`
			Deleted     int `json:"deleted"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, out.String())
	}
	if doc.Command != "clean" || doc.Delete {
		t.Errorf("document header = %+v", doc)
	}
	if doc.Summary.WouldDelete != 1 || doc.Summary.Deleted != 0 {
		t.Errorf("summary = %+v (dry run must delete nothing)", doc.Summary)
	}
	if len(doc.Results) != 1 || doc.Results[0].Decision != "would-delete" {
		t.Fatalf("results = %+v", doc.Results)
	}
	if doc.Results[0].Reason == "" {
		t.Error("a record must explain its decision")
	}
}

// TestJSONOutputIsAlwaysAnArray guards a consumer that iterates .results: an empty
// run must render [] rather than null.
func TestJSONOutputIsAlwaysAnArray(t *testing.T) {
	src, dest := t.TempDir(), t.TempDir() // no photos at all
	var out bytes.Buffer
	code := cli.Execute("dev",
		[]string{"sort", "--output=json", "--sample", "0", "--exiftool", stubExif(t), "--dest", dest, src},
		&out, io.Discard)
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), `"results":[]`) {
		t.Errorf("an empty run must render results as [], got:\n%s", out.String())
	}
}

func TestSortDryRunWritesNothing(t *testing.T) {
	args, dest := sortFixture(t, "--dry-run")
	var out bytes.Buffer
	if code := cli.Execute("dev", args, &out, io.Discard); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("--dry-run wrote into the destination: %v", entries)
	}
	// It must still report the work it would have done, flagged as a preview.
	for _, want := range []string{"copied=1", "companions_copied=1", "dry_run=true"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("dry-run summary missing %q; got: %s", want, out.String())
		}
	}
}

func TestVerbosityFlagsAreMutuallyExclusive(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{"quiet alone", []string{"--quiet"}, 0},
		{"verbose alone", []string{"--verbose"}, 0},
		{"quiet with verbose", []string{"--quiet", "--verbose"}, 2},
		{"quiet with log-level", []string{"--quiet", "--log-level", "debug"}, 2},
		{"verbose with log-level", []string{"--verbose", "--log-level", "warn"}, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args, _ := sortFixture(t, tc.args...)
			var errb bytes.Buffer
			if code := cli.Execute("dev", args, io.Discard, &errb); code != tc.want {
				t.Errorf("exit = %d, want %d; stderr=%s", code, tc.want, errb.String())
			}
		})
	}
}

func TestInvalidOutputFormatIsUsageError(t *testing.T) {
	for _, sub := range [][]string{
		{"sort", "--output", "yaml", "--sample", "0", t.TempDir()},
		{"clean", "--output", "yaml", t.TempDir()},
		{"version", "--output", "yaml"},
	} {
		t.Run(sub[0], func(t *testing.T) {
			if code := cli.Execute("dev", sub, io.Discard, io.Discard); code != 2 {
				t.Errorf("%v exit = %d, want 2 (usage)", sub, code)
			}
		})
	}
}

func TestVersionJSONOutput(t *testing.T) {
	var out bytes.Buffer
	if code := cli.Execute("1.2.3", []string{"version", "--output=json"}, &out, io.Discard); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var doc struct {
		Version   string `json:"version"`
		GoVersion string `json:"go_version"`
		Platform  string `json:"platform"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, out.String())
	}
	if doc.Version != "1.2.3" {
		t.Errorf("version = %q, want 1.2.3", doc.Version)
	}
	if doc.GoVersion == "" || doc.Platform == "" {
		t.Errorf("go version and platform are always knowable; got %+v", doc)
	}
}
