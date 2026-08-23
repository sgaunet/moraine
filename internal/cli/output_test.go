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

	t.Run("undo", func(t *testing.T) {
		_, dest := sortedFixture(t)

		var out, errb bytes.Buffer
		code := cli.Execute("dev", []string{"undo", dest}, &out, &errb)
		if code != 0 {
			t.Fatalf("exit = %d; stderr=%s", code, errb.String())
		}
		assertNoLogs(t, out.String())
		if !strings.Contains(errb.String(), "decision=would-remove") {
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
		"scanned=1", "unreadable=0",
		"groups=1", "copied=1", "skipped=0", "renamed=0", "errors=0",
		"bytes_skipped=0", "companions_bytes_skipped=0",
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
		Events []struct {
			Theme        string `json:"theme"`
			Method       string `json:"method"`
			Photos       int    `json:"photos"`
			Start        string `json:"start"`
			End          string `json:"end"`
			Copied       int    `json:"copied"`
			Skipped      int    `json:"skipped"`
			BytesCopied  int64  `json:"bytes_copied"`
			BytesSkipped int64  `json:"bytes_skipped"`
		} `json:"events"`
		Summary struct {
			Scanned                int   `json:"scanned"`
			Unreadable             int   `json:"unreadable"`
			Groups                 int   `json:"groups"`
			Copied                 int   `json:"copied"`
			CompanionsCopied       int   `json:"companions_copied"`
			BytesCopied            int64 `json:"bytes_copied"`
			BytesSkipped           int64 `json:"bytes_skipped"`
			CompanionsBytesCopied  int64 `json:"companions_bytes_copied"`
			CompanionsBytesSkipped int64 `json:"companions_bytes_skipped"`
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
	// The input tallies: one image found, all of it readable. A companion is not an
	// image the scan found, so it is not counted here.
	if doc.Summary.Scanned != 1 || doc.Summary.Unreadable != 0 {
		t.Errorf("scanned/unreadable = %d/%d; want 1/0", doc.Summary.Scanned, doc.Summary.Unreadable)
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
	// Volume: everything was copied, so nothing was spared, and the byte totals must
	// be real sizes rather than zero placeholders.
	if doc.Summary.BytesCopied <= 0 || doc.Summary.CompanionsBytesCopied <= 0 {
		t.Errorf("copied volume = %d photo / %d companion bytes; want both > 0",
			doc.Summary.BytesCopied, doc.Summary.CompanionsBytesCopied)
	}
	if doc.Summary.BytesSkipped != 0 || doc.Summary.CompanionsBytesSkipped != 0 {
		t.Errorf("nothing was skipped, so the spared volume must be 0; got %d / %d",
			doc.Summary.BytesSkipped, doc.Summary.CompanionsBytesSkipped)
	}
	// One event, described: its theme, how the theme was decided, and its cost.
	if len(doc.Events) != 1 {
		t.Fatalf("want 1 event, got %d: %+v", len(doc.Events), doc.Events)
	}
	ev := doc.Events[0]
	if ev.Theme == "" || ev.Method == "" || ev.Photos != 1 || ev.Start == "" || ev.End == "" {
		t.Errorf("incomplete event: %+v", ev)
	}
	// The event's counters cover the photo and its companion alike.
	if ev.Copied != 2 || ev.Skipped != 0 {
		t.Errorf("event copied/skipped = %d/%d; want 2/0", ev.Copied, ev.Skipped)
	}
	if ev.BytesCopied != doc.Summary.BytesCopied+doc.Summary.CompanionsBytesCopied {
		t.Errorf("the only event's copied volume (%d) must equal the run's (%d+%d)",
			ev.BytesCopied, doc.Summary.BytesCopied, doc.Summary.CompanionsBytesCopied)
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

func TestUndoJSONOutput(t *testing.T) {
	_, dest := sortedFixture(t)

	var out bytes.Buffer
	code := cli.Execute("dev", []string{"undo", "--output=json", dest}, &out, io.Discard)
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}

	var doc struct {
		Command string `json:"command"`
		Dest    string `json:"dest"`
		Delete  bool   `json:"delete"`
		Results []struct {
			Path     string `json:"path"`
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		} `json:"results"`
		Summary struct {
			Removed     int `json:"removed"`
			WouldRemove int `json:"would_remove"`
			DirsPruned  int `json:"dirs_pruned"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, out.String())
	}
	if doc.Command != "undo" || doc.Delete || doc.Dest != dest {
		t.Errorf("document header = %+v", doc)
	}
	if doc.Summary.WouldRemove != 2 || doc.Summary.Removed != 0 || doc.Summary.DirsPruned != 0 {
		t.Errorf("summary = %+v (a dry run must remove nothing)", doc.Summary)
	}
	if len(doc.Results) != 2 {
		t.Fatalf("results = %+v; want the photo and its companion", doc.Results)
	}
	for _, rec := range doc.Results {
		if rec.Decision != "would-remove" || rec.Reason == "" || rec.Path == "" {
			t.Errorf("record = %+v; want a would-remove decision with a path and a reason", rec)
		}
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
	if !strings.Contains(out.String(), `"events":[]`) {
		t.Errorf("an empty run must render events as [], got:\n%s", out.String())
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
		{"undo", "--output", "yaml", t.TempDir()},
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

// TestSortReportsTheVolumeItDidNotRecopy is the point of the byte counters: a re-run
// over an already-sorted library reports how much I/O it saved, which a count of
// skipped files cannot express — 12 skipped files could be 12 KB or 12 GB.
func TestSortReportsTheVolumeItDidNotRecopy(t *testing.T) {
	args, _ := sortFixture(t, "--output=json")
	// First run copies; second run finds both files identical and skips them.
	if code := cli.Execute("dev", args, io.Discard, io.Discard); code != 0 {
		t.Fatalf("first run: exit = %d", code)
	}
	var out bytes.Buffer
	if code := cli.Execute("dev", args, &out, io.Discard); code != 0 {
		t.Fatalf("second run: exit = %d", code)
	}

	var doc struct {
		Results []struct {
			Source string `json:"source"`
			Action string `json:"action"`
		} `json:"results"`
		Summary struct {
			Copied                 int   `json:"copied"`
			Skipped                int   `json:"skipped"`
			BytesCopied            int64 `json:"bytes_copied"`
			BytesSkipped           int64 `json:"bytes_skipped"`
			CompanionsSkipped      int   `json:"companions_skipped"`
			CompanionsBytesSkipped int64 `json:"companions_bytes_skipped"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, out.String())
	}
	if doc.Summary.Copied != 0 || doc.Summary.Skipped != 1 || doc.Summary.CompanionsSkipped != 1 {
		t.Fatalf("re-run summary = %+v; want nothing copied and both files skipped", doc.Summary)
	}
	if doc.Summary.BytesCopied != 0 {
		t.Errorf("a re-run wrote nothing, so bytes_copied must be 0; got %d", doc.Summary.BytesCopied)
	}
	// Cross-check the reported volume against the sources' real sizes on disk.
	var wantPhoto, wantCompanion int64
	for _, r := range doc.Results {
		info, err := os.Stat(r.Source)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Ext(r.Source) == ".png" {
			wantPhoto += info.Size()
		} else {
			wantCompanion += info.Size()
		}
	}
	if doc.Summary.BytesSkipped != wantPhoto {
		t.Errorf("bytes_skipped = %d, want %d (the photo's real size)", doc.Summary.BytesSkipped, wantPhoto)
	}
	if doc.Summary.CompanionsBytesSkipped != wantCompanion {
		t.Errorf("companions_bytes_skipped = %d, want %d", doc.Summary.CompanionsBytesSkipped, wantCompanion)
	}
}

// A dry run writes nothing, so its reported volume is what it *would* have written —
// otherwise the preview could not answer "how much will this copy?".
func TestSortDryRunReportsTheVolumeItWouldWrite(t *testing.T) {
	args, dest := sortFixture(t, "--output=json", "--dry-run")
	var out bytes.Buffer
	if code := cli.Execute("dev", args, &out, io.Discard); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var doc struct {
		Summary struct {
			BytesCopied           int64 `json:"bytes_copied"`
			CompanionsBytesCopied int64 `json:"companions_bytes_copied"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, out.String())
	}
	if doc.Summary.BytesCopied <= 0 || doc.Summary.CompanionsBytesCopied <= 0 {
		t.Errorf("a dry run must report the volume it would write; got %d / %d",
			doc.Summary.BytesCopied, doc.Summary.CompanionsBytesCopied)
	}
	// ... while still writing nothing at all.
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a dry run must write nothing, found %d entries in the destination", len(entries))
	}
}
