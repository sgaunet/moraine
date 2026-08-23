package manifest_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/manifest"
)

// runTime is a fixed clock: the run id is derived from it, so every expectation
// about file names stays deterministic.
var runTime = time.Date(2026, 8, 23, 9, 12, 5, 0, time.UTC)

func TestWriterCreatesNothingUntilFirstRecord(t *testing.T) {
	dest := t.TempDir()
	w := manifest.New(dest, "/src", runTime)
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if w.Path() != "" {
		t.Errorf("Path() = %q, want empty before the first record", w.Path())
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a record-less run wrote %d entries under the destination, want 0", len(entries))
	}
}

func TestNilWriterIsANoOp(t *testing.T) {
	var w *manifest.Writer
	if err := w.Add(manifest.Record{Source: "/a.jpg"}); err != nil {
		t.Errorf("Add on a nil Writer: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("Close on a nil Writer: %v", err)
	}
	if w.Path() != "" {
		t.Errorf("Path() on a nil Writer = %q, want empty", w.Path())
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	dest := t.TempDir()
	w := manifest.New(dest, "/src", runTime)
	records := []manifest.Record{
		{
			Source: "/src/IMG.jpg", Dest: filepath.Join(dest, "family/2026/2026-08-23/IMG.jpg"),
			Theme: "family", Date: "2026-08-23", Action: "copied", Size: 12, MTime: 1700000000,
		},
		{
			Source: "/src/IMG.jpg.xmp", Dest: filepath.Join(dest, "family/2026/2026-08-23/IMG.jpg.xmp"),
			Theme: "family", Date: "2026-08-23", Action: "renamed", Companion: true, Of: "/src/IMG.jpg",
			Size: 3, MTime: 1700000001,
		},
	}
	for _, r := range records {
		if err := w.Add(r); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	path, err := manifest.Latest(dest)
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if path != w.Path() {
		t.Errorf("Latest() = %q, want the written path %q", path, w.Path())
	}
	if got, want := filepath.Base(path), "20260823T091205Z.jsonl"; got != want {
		t.Errorf("manifest file name = %q, want %q", got, want)
	}

	run, err := manifest.ReadRun(path)
	if err != nil {
		t.Fatalf("read run: %v", err)
	}
	if run.Header.Manifest != manifest.SchemaVersion {
		t.Errorf("header schema = %d, want %d", run.Header.Manifest, manifest.SchemaVersion)
	}
	if run.Header.Source != "/src" || run.Header.Dest != dest {
		t.Errorf("header source/dest = %q/%q, want /src/%q", run.Header.Source, run.Header.Dest, dest)
	}
	if run.Header.Run != "20260823T091205Z" {
		t.Errorf("header run = %q", run.Header.Run)
	}
	if run.Skipped != 0 {
		t.Errorf("Skipped = %d, want 0", run.Skipped)
	}
	if len(run.Records) != len(records) {
		t.Fatalf("records = %d, want %d", len(run.Records), len(records))
	}
	for i, want := range records {
		if run.Records[i] != want {
			t.Errorf("record %d = %+v, want %+v", i, run.Records[i], want)
		}
	}
}

func TestTwoRunsInTheSameSecondGetDistinctFiles(t *testing.T) {
	dest := t.TempDir()
	first := manifest.New(dest, "/src", runTime)
	second := manifest.New(dest, "/src", runTime)
	for _, w := range []*manifest.Writer{first, second} {
		if err := w.Add(manifest.Record{Source: "/src/a.jpg", Dest: "/d/a.jpg", Action: "copied"}); err != nil {
			t.Fatalf("add: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
	}
	if first.Path() == second.Path() {
		t.Fatalf("both runs wrote to %q; a second run must never append to another's manifest", first.Path())
	}
	files, err := manifest.Files(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Errorf("Files() = %d, want 2", len(files))
	}
	// The suffixed run is the newer one. Ordering by file name would put it first
	// ('-' sorts before '.'), which would hand `undo` the wrong run.
	latest, err := manifest.Latest(dest)
	if err != nil {
		t.Fatal(err)
	}
	if latest != second.Path() {
		t.Errorf("Latest() = %q, want the second run %q", latest, second.Path())
	}
}

// writeRaw drops a manifest file straight onto disk: the on-disk format is a
// contract, so the reader is exercised against literal lines and not only against
// what the writer happens to produce.
func writeRaw(t *testing.T, dest, name, content string) string {
	t.Helper()
	dir := filepath.Join(dest, ".moraine", "runs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const header = `{"manifest":1,"run":"20260823T091205Z","source":"/src","dest":"/dest","started":"2026-08-23T09:12:05Z"}`

func TestReadRunSkipsUnparsableLines(t *testing.T) {
	dest := t.TempDir()
	// A killed run can leave a truncated final line; it must cost that line only.
	path := writeRaw(t, dest, "20260823T091205Z.jsonl", header+"\n"+
		`{"source":"/src/a.jpg","dest":"/dest/a.jpg","action":"copied"}`+"\n"+
		`{"source":"/src/b.jpg","de`+"\n")

	run, err := manifest.ReadRun(path)
	if err != nil {
		t.Fatalf("read run: %v", err)
	}
	if len(run.Records) != 1 || run.Records[0].Source != "/src/a.jpg" {
		t.Errorf("records = %+v, want the one complete record", run.Records)
	}
	if run.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", run.Skipped)
	}
}

func TestReadRunRejectsAForeignFile(t *testing.T) {
	dest := t.TempDir()
	for name, content := range map[string]string{
		"20260823T091205Z.jsonl": "not json at all\n",
		"20260823T091206Z.jsonl": `{"source":"/src/a.jpg"}` + "\n", // records but no header
		"20260823T091207Z.jsonl": "",
	} {
		if _, err := manifest.ReadRun(writeRaw(t, dest, name, content)); err == nil {
			t.Errorf("%s: expected an error reading a file with no manifest header", name)
		}
	}
}

func TestFilesAreOrderedAndUndoneRunsIgnored(t *testing.T) {
	dest := t.TempDir()
	writeRaw(t, dest, "20260823T091207Z.jsonl", header+"\n")
	writeRaw(t, dest, "20260823T091205Z.jsonl", header+"\n")
	writeRaw(t, dest, "20260823T091206Z.jsonl.undone", header+"\n")

	files, err := manifest.Files(dest)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, filepath.Base(f))
	}
	want := []string{"20260823T091205Z.jsonl", "20260823T091207Z.jsonl"}
	if len(names) != len(want) || names[0] != want[0] || names[1] != want[1] {
		t.Errorf("Files() = %v, want %v (oldest first, .undone ignored)", names, want)
	}

	latest, err := manifest.Latest(dest)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(latest) != "20260823T091207Z.jsonl" {
		t.Errorf("Latest() = %q, want the newest run", filepath.Base(latest))
	}
}

func TestLatestOnAnUnorganizedDestination(t *testing.T) {
	path, err := manifest.Latest(t.TempDir())
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if path != "" {
		t.Errorf("Latest() = %q, want empty when no run was ever recorded", path)
	}
}

func TestLoadFoldsRunsNewestWins(t *testing.T) {
	dest := t.TempDir()
	writeRaw(t, dest, "20260823T091205Z.jsonl", header+"\n"+
		`{"source":"/src/a.jpg","dest":"/dest/old/a.jpg","action":"copied","theme":"cook","size":1,"mtime":10}`+"\n"+
		`{"source":"/src/gone.jpg","action":"copied","error":"disk full"}`+"\n")
	writeRaw(t, dest, "20260823T091206Z.jsonl", header+"\n"+
		`{"source":"/src/a.jpg","dest":"/dest/new/a.jpg","action":"skipped-identical","theme":"family","size":1,"mtime":10}`+"\n")
	writeRaw(t, dest, "20260823T091207Z.jsonl.undone", header+"\n"+
		`{"source":"/src/undone.jpg","dest":"/dest/undone.jpg","action":"copied"}`+"\n")

	idx, err := manifest.Load(dest)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	rec, ok := idx.Lookup("/src/a.jpg")
	if !ok {
		t.Fatal("expected /src/a.jpg to be indexed")
	}
	if rec.Dest != "/dest/new/a.jpg" || rec.Theme != "family" {
		t.Errorf("indexed record = %+v, want the newest run's placement", rec)
	}
	if _, ok := idx.Lookup("/src/gone.jpg"); ok {
		t.Error("a failed placement must never be indexed as placed")
	}
	if _, ok := idx.Lookup("/src/undone.jpg"); ok {
		t.Error("an undone run must not be indexed")
	}
	if idx.Len() != 1 {
		t.Errorf("Len() = %d, want 1", idx.Len())
	}
}
