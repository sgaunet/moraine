package app_test

import (
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/app"
	"github.com/sgaunet/moraine/internal/manifest"
)

// readManifest returns the run recorded under dest, failing when there is none.
func readManifest(t *testing.T, dest string) manifest.Run {
	t.Helper()
	path, err := manifest.Latest(dest)
	if err != nil {
		t.Fatalf("latest manifest: %v", err)
	}
	if path == "" {
		t.Fatal("the run recorded no manifest")
	}
	run, err := manifest.ReadRun(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	return run
}

// recordFor returns the manifest record of one source path.
func recordFor(t *testing.T, run manifest.Run, source string) manifest.Record {
	t.Helper()
	for _, rec := range run.Records {
		if rec.Source == source {
			return rec
		}
	}
	t.Fatalf("no manifest record for %q (have %d records)", source, len(run.Records))
	return manifest.Record{}
}

// TestOrganizeRecordsEveryPlacement covers the manifest itself: one record per
// placed file — companions included — carrying where it went and the fingerprint
// `undo` and --incremental both check it against.
func TestOrganizeRecordsEveryPlacement(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	photoPath := filepath.Join(src, "a.png")
	sidecarPath := filepath.Join(src, "a.png.xmp")
	makePNG(t, photoPath)
	writeSidecar(t, sidecarPath, "appended")

	cfg := baseCfg(src, dest, true)
	cfg.Sidecars = true
	if _, err := app.Organize(context.Background(), cfg, quietLogger(), nil, nil); err != nil {
		t.Fatalf("Organize: %v", err)
	}

	run := readManifest(t, dest)
	if run.Header.Source != src || run.Header.Dest != dest {
		t.Errorf("header = %+v; want source %q and dest %q", run.Header, src, dest)
	}
	if len(run.Records) != 2 {
		t.Fatalf("records = %d, want one per placed file (photo + companion)", len(run.Records))
	}

	photo := recordFor(t, run, photoPath)
	if photo.Action != "copied" || photo.Theme != "other" || photo.Date != modTime.Format(manifest.DateFormat) {
		t.Errorf("photo record = %+v; want action copied, theme other, date %s",
			photo, modTime.Format(manifest.DateFormat))
	}
	if photo.Dest != filepath.Join(expectedDir(dest), "a.png") {
		t.Errorf("photo dest = %q, want %q", photo.Dest, filepath.Join(expectedDir(dest), "a.png"))
	}
	if photo.Companion || photo.Error != "" {
		t.Errorf("photo record must be a clean, non-companion placement: %+v", photo)
	}

	// The fingerprint has to describe the file that is really on disk, or undo
	// would refuse to remove what it just recorded.
	info, err := os.Lstat(photo.Dest)
	if err != nil {
		t.Fatal(err)
	}
	if photo.Size != info.Size() || photo.MTime != info.ModTime().UnixNano() {
		t.Errorf("recorded fingerprint (%d, %d) does not match the copy (%d, %d)",
			photo.Size, photo.MTime, info.Size(), info.ModTime().UnixNano())
	}

	companion := recordFor(t, run, sidecarPath)
	if !companion.Companion || companion.Of != photoPath {
		t.Errorf("companion record = %+v; want Companion=true Of=%q", companion, photoPath)
	}
}

// TestOrganizeDryRunRecordsNothing keeps the dry-run promise whole: a preview
// writes nothing at all, bookkeeping included.
func TestOrganizeDryRunRecordsNothing(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	makePNG(t, filepath.Join(src, "a.png"))

	cfg := baseCfg(src, dest, true)
	cfg.DryRun = true
	if _, err := app.Organize(context.Background(), cfg, quietLogger(), nil, nil); err != nil {
		t.Fatalf("Organize: %v", err)
	}

	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a dry run wrote %v under the destination, want nothing", entries)
	}
}

// TestOrganizeIncrementalSkipsWithoutAskingTheModel is the incremental payoff: a
// second pass over an unchanged library places nothing, compares no bytes, and
// makes no model call at all.
func TestOrganizeIncrementalSkipsWithoutAskingTheModel(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	makePNG(t, filepath.Join(src, "a.png"))

	chats := 0
	srv := ollamaStub(t, func(int) { chats++ })
	defer srv.Close()

	cfg := baseCfg(src, dest, true)
	cfg.Sample = 3
	cfg.Model = stubModel
	cfg.OllamaURL = srv.URL

	first, err := app.Organize(context.Background(), cfg, quietLogger(), nil, nil)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.Copied != 1 || chats == 0 {
		t.Fatalf("first run: summary %+v after %d model calls; want one copy and at least one call", first, chats)
	}
	mountainDir := filepath.Join(dest, "mountain", modTime.Format("2006"), modTime.Format(manifest.DateFormat))
	if _, err := os.Stat(filepath.Join(mountainDir, "a.png")); err != nil {
		t.Fatalf("first run should have filed the photo under the model's theme: %v", err)
	}

	callsBefore := chats
	cfg.Incremental = true
	second, err := app.Organize(context.Background(), cfg, quietLogger(), nil, nil)
	if err != nil {
		t.Fatalf("incremental run: %v", err)
	}
	if second.Copied != 0 || second.Skipped != 1 {
		t.Errorf("incremental summary = %+v; want Copied=0 Skipped=1", second)
	}
	if chats != callsBefore {
		t.Errorf("incremental run made %d model calls, want none", chats-callsBefore)
	}
}

// TestOrganizeIncrementalKeepsANewPhotoWithItsEvent covers the theme-reuse half:
// a photo added to an already-filed event inherits that event's theme from the
// manifest instead of being classified on its own — and no model call is needed
// to decide it.
func TestOrganizeIncrementalKeepsANewPhotoWithItsEvent(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	makePNG(t, filepath.Join(src, "a.png"))

	srv := ollamaStub(t, nil)
	cfg := baseCfg(src, dest, true)
	cfg.Sample = 3
	cfg.Model = stubModel
	cfg.OllamaURL = srv.URL
	if _, err := app.Organize(context.Background(), cfg, quietLogger(), nil, nil); err != nil {
		t.Fatalf("first run: %v", err)
	}
	srv.Close()

	// A stub that fails the test if it is consulted at all.
	strict := ollamaStub(t, func(int) { t.Error("the model was asked about an already-filed event") })
	defer strict.Close()
	cfg.OllamaURL = strict.URL
	cfg.Incremental = true

	makePNG(t, filepath.Join(src, "b.png")) // same event: same capture time, same gap
	second, err := app.Organize(context.Background(), cfg, quietLogger(), nil, nil)
	if err != nil {
		t.Fatalf("incremental run: %v", err)
	}
	if second.Copied != 1 || second.Skipped != 1 {
		t.Errorf("incremental summary = %+v; want Copied=1 (the new photo) Skipped=1 (the known one)", second)
	}
	mountainDir := filepath.Join(dest, "mountain", modTime.Format("2006"), modTime.Format(manifest.DateFormat))
	if _, err := os.Stat(filepath.Join(mountainDir, "b.png")); err != nil {
		t.Errorf("the new photo should have joined its event's theme: %v", err)
	}
}

// TestOrganizeIncrementalRefusesToTrustAChangedSource is the safety half: the
// manifest is a shortcut, never an authority. A source edited since it was placed
// goes through the full comparison, which is what turns it into a rename.
func TestOrganizeIncrementalRefusesToTrustAChangedSource(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	photoPath := filepath.Join(src, "a.png")
	makePNG(t, photoPath)

	cfg := baseCfg(src, dest, true)
	if _, err := app.Organize(context.Background(), cfg, quietLogger(), nil, nil); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Different pixels, different size, later mtime — the same file name.
	makeWiderPNG(t, photoPath, modTime.Add(time.Hour))

	cfg.Incremental = true
	second, err := app.Organize(context.Background(), cfg, quietLogger(), nil, nil)
	if err != nil {
		t.Fatalf("incremental run: %v", err)
	}
	if second.Renamed != 1 || second.Skipped != 0 {
		t.Fatalf("incremental summary = %+v; want Renamed=1 Skipped=0", second)
	}
}

// makeWiderPNG overwrites path with a larger PNG stamped at mt, so it differs from
// the original in both content and fingerprint.
func makeWiderPNG(t *testing.T, path string, mt time.Time) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, 64, 64))); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mt, mt); err != nil {
		t.Fatal(err)
	}
}
