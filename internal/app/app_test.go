package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/app"
	"github.com/sgaunet/moraine/internal/classify"
	"github.com/sgaunet/moraine/internal/config"
	"github.com/sgaunet/moraine/internal/exiftooltest"
	"github.com/sgaunet/moraine/internal/organize"
)

// safeBuffer is a concurrency-safe sink for slog output (readMeta logs from workers).
type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

var modTime = time.Date(2025, 8, 12, 12, 0, 0, 0, time.UTC)

func makePNG(t *testing.T, path string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

func baseCfg(source, dest string, isDir bool) config.Config {
	return config.Config{
		Source:        source,
		SourceIsDir:   isDir,
		DestRoot:      dest,
		Gap:           time.Hour,
		Sample:        0, // disable the model → deterministic fallback theme "other"
		Themes:        []string{"family", "mountain", "special-events", "nature"},
		FallbackTheme: "other",
		LogLevel:      slog.LevelInfo,
	}
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&safeBuffer{}, &slog.HandlerOptions{Level: slog.LevelError}))
}

func expectedDir(dest string) string {
	return filepath.Join(dest, "other", modTime.Format("2006"), modTime.Format("2006-01-02"))
}

func TestOrganizeBatchCopiesAndIsIdempotent(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	makePNG(t, filepath.Join(src, "a.png"))
	makePNG(t, filepath.Join(src, "b.png"))
	cfg := baseCfg(src, dest, true)

	sum, err := app.Organize(context.Background(), cfg, quietLogger(), nil)
	if err != nil {
		t.Fatalf("Organize: %v", err)
	}
	if sum.Groups != 1 || sum.Copied != 2 {
		t.Fatalf("summary = %+v; want Groups=1 Copied=2", sum)
	}
	for _, n := range []string{"a.png", "b.png"} {
		if _, err := os.Stat(filepath.Join(expectedDir(dest), n)); err != nil {
			t.Errorf("missing %s in dest: %v", n, err)
		}
		if _, err := os.Stat(filepath.Join(src, n)); err != nil {
			t.Errorf("original %s must be preserved: %v", n, err)
		}
	}

	// Re-run → identical files skipped.
	sum2, err := app.Organize(context.Background(), cfg, quietLogger(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if sum2.Copied != 0 || sum2.Skipped != 2 {
		t.Fatalf("re-run summary = %+v; want Copied=0 Skipped=2", sum2)
	}
}

func TestOrganizeSinglePhoto(t *testing.T) {
	dir := t.TempDir()
	dest := t.TempDir()
	file := filepath.Join(dir, "single.png")
	makePNG(t, file)
	cfg := baseCfg(file, dest, false)

	sum, err := app.Organize(context.Background(), cfg, quietLogger(), nil)
	if err != nil {
		t.Fatalf("Organize: %v", err)
	}
	if sum.Groups != 1 || sum.Copied != 1 {
		t.Fatalf("summary = %+v; want Groups=1 Copied=1", sum)
	}
	if _, err := os.Stat(filepath.Join(expectedDir(dest), "single.png")); err != nil {
		t.Errorf("single photo not placed: %v", err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Errorf("original must be preserved: %v", err)
	}
}

func TestOrganizeSkipsNonImageFiles(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	makePNG(t, filepath.Join(src, "a.png"))
	if err := os.WriteFile(filepath.Join(src, "notes.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	sum, err := app.Organize(context.Background(), baseCfg(src, dest, true), quietLogger(), nil)
	if err != nil {
		t.Fatalf("Organize: %v", err)
	}
	if sum.Copied != 1 {
		t.Fatalf("Copied = %d; want 1 (non-image ignored)", sum.Copied)
	}
	if _, err := os.Stat(filepath.Join(expectedDir(dest), "notes.txt")); !os.IsNotExist(err) {
		t.Error("non-image must not be placed")
	}
}

func TestOrganizeContinuesOnUnreadableImage(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are ignored")
	}
	src := t.TempDir()
	dest := t.TempDir()
	makePNG(t, filepath.Join(src, "ok.png"))
	bad := filepath.Join(src, "bad.jpg")
	if err := os.WriteFile(bad, []byte("x"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0o644) })

	buf := &safeBuffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	sum, err := app.Organize(context.Background(), baseCfg(src, dest, true), logger, nil)
	if err != nil {
		t.Fatalf("run must not abort on an unreadable file: %v", err)
	}
	if sum.Copied != 1 {
		t.Fatalf("Copied = %d; want 1 (unreadable skipped)", sum.Copied)
	}
	// A skipped file is in no placement counter, so without these two the run's
	// result would not say it had been dropped at all.
	if sum.Scanned != 2 || sum.Unreadable != 1 {
		t.Errorf("Scanned/Unreadable = %d/%d; want 2/1", sum.Scanned, sum.Unreadable)
	}
	if !strings.Contains(buf.String(), "file skipped") {
		t.Error("expected a warning log for the unreadable file")
	}
}

// TestOrganizeLoggingContract pins what each verbosity level shows. Per-file lines
// live at debug on purpose: a real library produces thousands of them, and the run's
// result is carried by the Summary the transport prints to stdout. The group and
// classification narrative stays at info, so a default run still explains itself.
func TestOrganizeLoggingContract(t *testing.T) {
	organizeAt := func(t *testing.T, level slog.Level) string {
		t.Helper()
		src, dest := t.TempDir(), t.TempDir()
		makePNG(t, filepath.Join(src, "a.png"))
		buf := &safeBuffer{}
		logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: level}))
		if _, err := app.Organize(context.Background(), baseCfg(src, dest, true), logger, nil); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}

	t.Run("info narrates groups but not files", func(t *testing.T) {
		out := organizeAt(t, slog.LevelInfo)
		for _, want := range []string{"group", "method", "theme"} {
			if !strings.Contains(out, want) {
				t.Errorf("info log missing %q\n---\n%s", want, out)
			}
		}
		for _, unwanted := range []string{"msg=photo", "action=copied"} {
			if strings.Contains(out, unwanted) {
				t.Errorf("info log must stay quiet about individual files, found %q\n---\n%s", unwanted, out)
			}
		}
	})

	t.Run("debug adds every file and the summary", func(t *testing.T) {
		out := organizeAt(t, slog.LevelDebug)
		for _, want := range []string{"group", "msg=photo", "action=copied", "dest=", "summary"} {
			if !strings.Contains(out, want) {
				t.Errorf("debug log missing %q\n---\n%s", want, out)
			}
		}
	})
}

// The free-space preflight has to be right about two things: the byte total it
// estimates, and its silence when the destination is fine. A signed/unsigned slip in
// the comparison would nag every user on every run, which is why the absence of the
// warning is asserted as firmly as the figure is.
func TestOrganizeFreeSpacePreflight(t *testing.T) {
	src, dest := t.TempDir(), t.TempDir()
	var want int64
	for _, name := range []string{"a.png", "b.png"} {
		path := filepath.Join(src, name)
		makePNG(t, path)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		want += info.Size()
	}

	buf := &safeBuffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if _, err := app.Organize(context.Background(), baseCfg(src, dest, true), logger, nil); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	// The estimate has to be the scan's own byte total: proof that Found.Size is
	// summed, and that statfs answered for a destination directory that exists.
	if wantLine := fmt.Sprintf("msg=space needed_bytes=%d", want); !strings.Contains(out, wantLine) {
		t.Errorf("debug log missing %q\n---\n%s", wantLine, out)
	}
	if !strings.Contains(out, "available_bytes=") {
		t.Errorf("space line reports no available_bytes\n---\n%s", out)
	}
	if strings.Contains(out, "too small") {
		t.Errorf("a temp directory warned about free space\n---\n%s", out)
	}
}

// A destination that does not exist yet is the normal first run: organize creates it
// on first use, so the preflight must answer for the filesystem that will host it
// rather than give up.
func TestOrganizeFreeSpacePreflightOnUncreatedDestination(t *testing.T) {
	src := t.TempDir()
	dest := filepath.Join(t.TempDir(), "sorted", "library") // neither level exists
	makePNG(t, filepath.Join(src, "a.png"))

	buf := &safeBuffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if _, err := app.Organize(context.Background(), baseCfg(src, dest, true), logger, nil); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "msg=space") || strings.Contains(out, "free space unknown") {
		t.Errorf("preflight gave up on a not-yet-created destination\n---\n%s", out)
	}
}

func TestOrganizeOllamaUnreachableWarnsAndFallsBack(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	makePNG(t, filepath.Join(src, "a.png"))

	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := closed.URL
	closed.Close() // refuse connections → preflight reports unreachable

	cfg := baseCfg(src, dest, true)
	cfg.Sample = 3
	cfg.Model = stubModel
	cfg.OllamaURL = url

	buf := &safeBuffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	sum, err := app.Organize(context.Background(), cfg, logger, nil)
	if err != nil {
		t.Fatalf("run must not abort when Ollama is unreachable: %v", err)
	}
	if sum.Copied != 1 {
		t.Fatalf("Copied = %d; want 1 (placed via fallback)", sum.Copied)
	}
	if !strings.Contains(buf.String(), "Ollama unreachable") {
		t.Errorf("expected an actionable 'unreachable' warning, got:\n%s", buf.String())
	}
}

func TestOrganizeOllamaModelMissingTellsToPull(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	makePNG(t, filepath.Join(src, "a.png"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"name":"llama3:8b"}]}`)) // configured model absent
	}))
	defer srv.Close()

	cfg := baseCfg(src, dest, true)
	cfg.Sample = 3
	cfg.Model = stubModel
	cfg.OllamaURL = srv.URL

	buf := &safeBuffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	sum, err := app.Organize(context.Background(), cfg, logger, nil)
	if err != nil {
		t.Fatalf("run must not abort when the model is missing: %v", err)
	}
	if sum.Copied != 1 {
		t.Fatalf("Copied = %d; want 1 (placed via fallback)", sum.Copied)
	}
	out := buf.String()
	if !strings.Contains(out, "ollama pull qwen3-vl:8b") {
		t.Errorf("expected an actionable 'ollama pull' message, got:\n%s", out)
	}
}

func TestOrganizeLogLevelWarnSuppressesInfo(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	makePNG(t, filepath.Join(src, "a.png"))

	buf := &safeBuffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if _, err := app.Organize(context.Background(), baseCfg(src, dest, true), logger, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "group") {
		t.Errorf("warn level must suppress info lines, got:\n%s", buf.String())
	}
}

// makeRAW writes a dummy RAW file (content is not a real RAW; exifmeta falls back
// to mtime, which is what determines the destination date).
func makeRAW(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("not-a-real-raw"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

// stubModel is the vision model every Ollama stub advertises; tests point
// cfg.Model at it so the preflight finds what it looks for.
const stubModel = "qwen3-vl:8b"

// ollamaStub serves /api/tags (advertising stubModel) and /api/chat (always
// answering "mountain"), invoking onChat with the number of images received.
func ollamaStub(t *testing.T, onChat func(images int)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(`{"models":[{"name":"` + stubModel + `"}]}`))
			return
		}
		var body struct {
			Messages []struct {
				Images []string `json:"images"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		n := 0
		for _, m := range body.Messages {
			n += len(m.Images)
		}
		if onChat != nil {
			onChat(n)
		}
		_, _ = w.Write([]byte(`{"message":{"content":"mountain"}}`))
	}))
}

func TestOrganizeRAWCopiedAndDated(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	makeRAW(t, filepath.Join(src, "shot.dng"))

	// Sample 0 → no model; RAW is still recognized, dated, and copied (fallback theme).
	sum, err := app.Organize(context.Background(), baseCfg(src, dest, true), quietLogger(), nil)
	if err != nil {
		t.Fatalf("Organize: %v", err)
	}
	if sum.Copied != 1 {
		t.Fatalf("Copied = %d; want 1 (RAW copied)", sum.Copied)
	}
	if _, err := os.Stat(filepath.Join(expectedDir(dest), "shot.dng")); err != nil {
		t.Errorf("RAW not placed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(src, "shot.dng")); err != nil {
		t.Errorf("original RAW must be preserved: %v", err)
	}
}

func TestOrganizeSingleRAWPhoto(t *testing.T) {
	dir := t.TempDir()
	dest := t.TempDir()
	file := filepath.Join(dir, "single.nef")
	makeRAW(t, file)

	sum, err := app.Organize(context.Background(), baseCfg(file, dest, false), quietLogger(), nil)
	if err != nil {
		t.Fatalf("Organize: %v", err)
	}
	if sum.Groups != 1 || sum.Copied != 1 {
		t.Fatalf("summary = %+v; want Groups=1 Copied=1", sum)
	}
	if _, err := os.Stat(filepath.Join(expectedDir(dest), "single.nef")); err != nil {
		t.Errorf("single RAW not placed: %v", err)
	}
}

func TestOrganizeRAWClassifiedViaPreview(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	makeRAW(t, filepath.Join(src, "peak.dng"))

	exifPath, err := exiftooltest.Stub(t.TempDir(), exiftooltest.Options{
		Previews: map[string][]byte{"JpgFromRaw": []byte("PREVIEW")},
	})
	if err != nil {
		t.Fatal(err)
	}
	var gotImages int
	srv := ollamaStub(t, func(n int) { gotImages = n })
	defer srv.Close()

	cfg := baseCfg(src, dest, true)
	cfg.Sample = 3
	cfg.Model = stubModel
	cfg.OllamaURL = srv.URL
	cfg.ExifToolPath = exifPath

	sum, err := app.Organize(context.Background(), cfg, quietLogger(), nil)
	if err != nil {
		t.Fatalf("Organize: %v", err)
	}
	if sum.Copied != 1 {
		t.Fatalf("Copied = %d; want 1", sum.Copied)
	}
	if gotImages < 1 {
		t.Error("the model received no image for the RAW (preview not wired through)")
	}
	mountainDir := filepath.Join(dest, "mountain", modTime.Format("2006"), modTime.Format("2006-01-02"))
	if _, err := os.Stat(filepath.Join(mountainDir, "peak.dng")); err != nil {
		t.Errorf("RAW not placed under the preview-classified theme 'mountain': %v", err)
	}
}

// TestOrganizeRAWPreservesOriginalAndLeavesNoTemp covers US3 at the app level
// (SC-003, SC-005, SC-006): the original RAW is byte-identical after the run and
// no preview artifact is written to the temp area.
func TestOrganizeRAWPreservesOriginalAndLeavesNoTemp(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	rawPath := filepath.Join(src, "shot.dng")
	makeRAW(t, rawPath)
	before, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatal(err)
	}

	exifPath, err := exiftooltest.Stub(t.TempDir(), exiftooltest.Options{
		Previews: map[string][]byte{"JpgFromRaw": []byte("PREVIEW")},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := ollamaStub(t, nil)
	defer srv.Close()

	cfg := baseCfg(src, dest, true)
	cfg.Sample = 3
	cfg.Model = stubModel
	cfg.OllamaURL = srv.URL
	cfg.ExifToolPath = exifPath

	// Redirect TMPDIR only after all helper temp dirs/stubs exist, so a stray
	// preview temp write (there should be none) would land in the monitored dir.
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	if _, err := app.Organize(context.Background(), cfg, quietLogger(), nil); err != nil {
		t.Fatalf("Organize: %v", err)
	}

	after, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("original RAW must be byte-identical after the run")
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("temp dir not empty after run: %v (previews must stay in memory)", entries)
	}
}

// writeSidecar writes a non-image companion file beside a photo.
func writeSidecar(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestOrganizeCopiesCompanions covers US1 at the app level: a photo's appended and
// base-name companions are copied into the same destination folder and tallied
// distinctly from photos (FR-001, FR-010, SC-001).
func TestOrganizeCopiesCompanions(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	makePNG(t, filepath.Join(src, "a.png"))
	writeSidecar(t, filepath.Join(src, "a.png.xmp"), "appended")
	writeSidecar(t, filepath.Join(src, "a.xmp"), "base")

	cfg := baseCfg(src, dest, true)
	cfg.Sidecars = true
	sum, err := app.Organize(context.Background(), cfg, quietLogger(), nil)
	if err != nil {
		t.Fatalf("Organize: %v", err)
	}
	if sum.Copied != 1 || sum.CompanionsCopied != 2 {
		t.Fatalf("summary = %+v; want Copied=1 CompanionsCopied=2", sum)
	}
	for _, n := range []string{"a.png.xmp", "a.xmp"} {
		if _, err := os.Stat(filepath.Join(expectedDir(dest), n)); err != nil {
			t.Errorf("companion %s not placed: %v", n, err)
		}
		if _, err := os.Stat(filepath.Join(src, n)); err != nil {
			t.Errorf("source companion %s must be preserved: %v", n, err)
		}
	}
}

// TestOrganizeSingleFileCompanions covers FR-011: companion copying also applies
// when sorting a single file.
func TestOrganizeSingleFileCompanions(t *testing.T) {
	dir := t.TempDir()
	dest := t.TempDir()
	file := filepath.Join(dir, "single.png")
	makePNG(t, file)
	writeSidecar(t, filepath.Join(dir, "single.png.xmp"), "appended")
	writeSidecar(t, filepath.Join(dir, "single.xmp"), "base")

	cfg := baseCfg(file, dest, false)
	cfg.Sidecars = true
	sum, err := app.Organize(context.Background(), cfg, quietLogger(), nil)
	if err != nil {
		t.Fatalf("Organize: %v", err)
	}
	if sum.Copied != 1 || sum.CompanionsCopied != 2 {
		t.Fatalf("summary = %+v; want Copied=1 CompanionsCopied=2", sum)
	}
	for _, n := range []string{"single.png.xmp", "single.xmp"} {
		if _, err := os.Stat(filepath.Join(expectedDir(dest), n)); err != nil {
			t.Errorf("single-file companion %s not placed: %v", n, err)
		}
	}
}

// TestOrganizeCompanionThatIsAnImageIsSortedOnce covers FR-006: a companion-named
// file that is itself a recognized image is sorted as its own primary photo and is
// not additionally copied as a companion.
func TestOrganizeCompanionThatIsAnImageIsSortedOnce(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	makePNG(t, filepath.Join(src, "a.png"))
	makePNG(t, filepath.Join(src, "a.png.png")) // matches a.png as an appended companion, but is an image

	cfg := baseCfg(src, dest, true)
	cfg.Sidecars = true
	sum, err := app.Organize(context.Background(), cfg, quietLogger(), nil)
	if err != nil {
		t.Fatalf("Organize: %v", err)
	}
	if sum.Copied != 2 {
		t.Fatalf("Copied = %d; want 2 (both images sorted as primaries)", sum.Copied)
	}
	if sum.CompanionsCopied != 0 {
		t.Fatalf("CompanionsCopied = %d; want 0 (an image is never a companion, FR-006)", sum.CompanionsCopied)
	}
}

// TestOrganizeCompanionFailureNonFatal covers FR-008: a companion that cannot be
// copied is tallied as a companion error and does not abort the run.
func TestOrganizeCompanionFailureNonFatal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are ignored")
	}
	src := t.TempDir()
	dest := t.TempDir()
	makePNG(t, filepath.Join(src, "a.png"))
	bad := filepath.Join(src, "a.xmp")
	if err := os.WriteFile(bad, []byte("base"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0o644) })

	cfg := baseCfg(src, dest, true)
	cfg.Sidecars = true
	sum, err := app.Organize(context.Background(), cfg, quietLogger(), nil)
	if err != nil {
		t.Fatalf("run must not abort on a companion failure: %v", err)
	}
	if sum.Copied != 1 {
		t.Fatalf("Copied = %d; want 1 (photo still placed)", sum.Copied)
	}
	if sum.CompanionsErrors != 1 {
		t.Fatalf("CompanionsErrors = %d; want 1 (summary=%+v)", sum.CompanionsErrors, sum)
	}
}

// TestOrganizeJobs checks that --jobs only changes how many workers read EXIF, never
// what the run produces. 0 means "one per CPU"; 1 serialises it.
func TestOrganizeJobs(t *testing.T) {
	run := func(t *testing.T, jobs int) app.Summary {
		t.Helper()
		src, dest := t.TempDir(), t.TempDir()
		for _, n := range []string{"a.png", "b.png", "c.png", "d.png"} {
			makePNG(t, filepath.Join(src, n))
		}
		cfg := baseCfg(src, dest, true)
		cfg.Jobs = jobs
		sum, err := app.Organize(context.Background(), cfg, quietLogger(), nil)
		if err != nil {
			t.Fatalf("jobs=%d: %v", jobs, err)
		}
		return sum
	}

	auto, serial := run(t, 0), run(t, 1)
	// DeepEqual, not ==: Summary now carries a per-event slice. That makes this a
	// stronger determinism check than before — the events themselves must match too.
	if !reflect.DeepEqual(auto, serial) {
		t.Errorf("worker count changed the outcome:\n jobs=0 %+v\n jobs=1 %+v", auto, serial)
	}
	if auto.Copied != 4 {
		t.Errorf("copied = %d, want 4", auto.Copied)
	}
}

// TestOrganizeInterruptDoesNotInflateErrors is the regression test for the interrupt
// defect: organize.Place records the context error against every photo it never reached,
// and those used to be tallied as placement failures — so a Ctrl-C on a large library
// reported thousands of "errors" for photos that were simply never attempted.
func TestOrganizeInterruptDoesNotInflateErrors(t *testing.T) {
	src, dest := t.TempDir(), t.TempDir()
	for _, n := range []string{"a.png", "b.png", "c.png", "d.png", "e.png"} {
		makePNG(t, filepath.Join(src, n))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled: nothing will be attempted

	buf := &safeBuffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	sum, err := app.Organize(ctx, baseCfg(src, dest, true), logger, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if sum.Errors != 0 {
		t.Errorf("Errors = %d, want 0: a cancelled run attempted nothing, so nothing failed", sum.Errors)
	}
	if strings.Contains(buf.String(), "placement failed") {
		t.Errorf("an interrupt must not be logged as a placement failure\n---\n%s", buf.String())
	}
}

// TestOrganizeOnResultSeesEveryPlacement pins the callback the transport uses to
// render its machine-readable stdout: one record per placement, photos and
// companions alike, matching the summary counters.
func TestOrganizeOnResultSeesEveryPlacement(t *testing.T) {
	src, dest := t.TempDir(), t.TempDir()
	makePNG(t, filepath.Join(src, "a.png"))
	if err := os.WriteFile(filepath.Join(src, "a.png.xmp"), []byte("sidecar"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := baseCfg(src, dest, true)
	cfg.Sidecars = true

	var photos, companions int
	sum, err := app.Organize(context.Background(), cfg, quietLogger(), func(r organize.Result) {
		if r.IsCompanion {
			companions++
			if r.Of == "" {
				t.Error("a companion record must name the photo it belongs to")
			}
			return
		}
		photos++
	})
	if err != nil {
		t.Fatal(err)
	}
	if photos != 1 || companions != 1 {
		t.Errorf("records: %d photos, %d companions; want 1 and 1", photos, companions)
	}
	if sum.Copied != photos || sum.CompanionsCopied != companions {
		t.Errorf("records disagree with the summary: %+v", sum)
	}
}

// TestOrganizeReportsInterruptInTheLastCluster covers the case the between-clusters
// check cannot see: a single-event import, where one cluster holds every photo. The
// cancellation arrives while that cluster is being placed, so the loop never gets a
// second iteration to notice it — the run would otherwise finish "successfully"
// having placed a few photos and abandoned the rest.
func TestOrganizeReportsInterruptInTheLastCluster(t *testing.T) {
	src, dest := t.TempDir(), t.TempDir()
	for _, n := range []string{"a.png", "b.png", "c.png"} {
		makePNG(t, filepath.Join(src, n)) // one modTime ⇒ exactly one cluster
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Cancel once the first record lands: the cluster has already been placed, so
	// only the post-loop check can still report it.
	var records int
	sum, err := app.Organize(ctx, baseCfg(src, dest, true), quietLogger(), func(organize.Result) {
		records++
		cancel()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if sum.Groups != 1 {
		t.Fatalf("want the one-cluster setup, got %d groups", sum.Groups)
	}
	if records == 0 {
		t.Error("the run should have reported the placements it did complete")
	}
	if sum.Errors != 0 {
		t.Errorf("Errors = %d, want 0", sum.Errors)
	}
}

// TestOrganizeHonoursPathTemplate proves the template reaches the Organizer through
// the whole pipeline, not just in organize's own unit tests. Every other test in
// this file leaves PathTemplate zero and asserts expectedDir, which is what pins
// that the default layout is unchanged.
func TestOrganizeHonoursPathTemplate(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	makePNG(t, filepath.Join(src, "a.png"))

	cfg := baseCfg(src, dest, true)
	tmpl, err := organize.ParseTemplate("{year}/{month}/{theme}")
	if err != nil {
		t.Fatalf("ParseTemplate: %v", err)
	}
	cfg.PathTemplate = tmpl

	sum, err := app.Organize(context.Background(), cfg, quietLogger(), nil)
	if err != nil {
		t.Fatalf("Organize: %v", err)
	}
	if sum.Copied != 1 {
		t.Fatalf("summary = %+v; want Copied=1", sum)
	}
	want := filepath.Join(dest, modTime.Format("2006"), modTime.Format("01"), "other", "a.png")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected %q: %v", want, err)
	}
	if _, err := os.Stat(expectedDir(dest)); err == nil {
		t.Error("the default layout was created as well")
	}
}

// An incremental re-run under a different template must not re-copy anything, and
// must not leave the new layout's folders behind either: the recorded files keep the
// paths they already have. The run warns about this on stderr (see placedIndex).
func TestOrganizeIncrementalUnderAChangedTemplateKeepsRecordedPaths(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	makePNG(t, filepath.Join(src, "a.png"))

	cfg := baseCfg(src, dest, true)
	if _, err := app.Organize(context.Background(), cfg, quietLogger(), nil); err != nil {
		t.Fatalf("first run: %v", err)
	}
	firstDest := filepath.Join(expectedDir(dest), "a.png")
	if _, err := os.Stat(firstDest); err != nil {
		t.Fatalf("first run did not place the photo: %v", err)
	}

	changed := baseCfg(src, dest, true)
	changed.Incremental = true
	tmpl, err := organize.ParseTemplate("{year}/{month}/{theme}")
	if err != nil {
		t.Fatalf("ParseTemplate: %v", err)
	}
	changed.PathTemplate = tmpl

	sum, err := app.Organize(context.Background(), changed, quietLogger(), nil)
	if err != nil {
		t.Fatalf("incremental run: %v", err)
	}
	if sum.Copied != 0 || sum.Skipped != 1 {
		t.Fatalf("summary = %+v; want Copied=0 Skipped=1", sum)
	}
	if _, err := os.Stat(firstDest); err != nil {
		t.Errorf("the recorded copy must stay where it was: %v", err)
	}
	newLayout := filepath.Join(dest, modTime.Format("2006"), modTime.Format("01"), "other")
	if _, err := os.Stat(newLayout); err == nil {
		t.Errorf("the new layout's folder %q was created for a fully skipped run", newLayout)
	}
}

// TestOrganizeDescribesEachEvent covers what the per-event report exists for: which
// events a run produced, how each theme was decided, and what each event cost. Until
// now the method was written to one log line and discarded.
func TestOrganizeDescribesEachEvent(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	makePNG(t, filepath.Join(src, "a.png"))
	makePNG(t, filepath.Join(src, "b.png"))
	// Push b.png well past the gap so it forms its own event.
	later := modTime.Add(48 * time.Hour)
	if err := os.Chtimes(filepath.Join(src, "b.png"), later, later); err != nil {
		t.Fatal(err)
	}
	cfg := baseCfg(src, dest, true)

	sum, err := app.Organize(context.Background(), cfg, quietLogger(), nil)
	if err != nil {
		t.Fatalf("Organize: %v", err)
	}
	if len(sum.Events) != sum.Groups || sum.Groups != 2 {
		t.Fatalf("events = %d, groups = %d; want 2 of each", len(sum.Events), sum.Groups)
	}

	var copied int
	var volume int64
	for _, ev := range sum.Events {
		if ev.Theme == "" || ev.Method == "" || ev.Photos != 1 {
			t.Errorf("incomplete event: %+v", ev)
		}
		if ev.Start.IsZero() || ev.End.Before(ev.Start) {
			t.Errorf("event span is not coherent: %+v", ev)
		}
		if ev.BytesCopied <= 0 {
			t.Errorf("event reported no copied volume: %+v", ev)
		}
		copied += ev.Copied
		volume += ev.BytesCopied
	}
	// The events must account for exactly what the run's own totals say.
	if copied != sum.Copied {
		t.Errorf("events copied %d files, run says %d", copied, sum.Copied)
	}
	if volume != sum.BytesCopied {
		t.Errorf("events copied %d bytes, run says %d", volume, sum.BytesCopied)
	}
	// Events come out in placement order, which is capture-time order.
	if sum.Events[0].Start.After(sum.Events[1].Start) {
		t.Error("events are not in capture-time order")
	}
}

// An incremental re-run reuses the theme the manifest recorded, so its events must
// say so: method=manifest is the evidence that no model call was made.
func TestOrganizeEventsReportTheManifestMethodOnAReRun(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	makePNG(t, filepath.Join(src, "a.png"))
	cfg := baseCfg(src, dest, true)

	if _, err := app.Organize(context.Background(), cfg, quietLogger(), nil); err != nil {
		t.Fatalf("first run: %v", err)
	}
	inc := baseCfg(src, dest, true)
	inc.Incremental = true
	sum, err := app.Organize(context.Background(), inc, quietLogger(), nil)
	if err != nil {
		t.Fatalf("incremental run: %v", err)
	}
	if len(sum.Events) != 1 {
		t.Fatalf("want 1 event, got %d", len(sum.Events))
	}
	ev := sum.Events[0]
	if ev.Method != string(classify.MethodManifest) {
		t.Errorf("method = %q, want %q", ev.Method, classify.MethodManifest)
	}
	// Nothing was rewritten, and the spared volume is reported instead.
	if ev.Copied != 0 || ev.Skipped != 1 {
		t.Errorf("event copied/skipped = %d/%d; want 0/1", ev.Copied, ev.Skipped)
	}
	if ev.BytesCopied != 0 || ev.BytesSkipped <= 0 {
		t.Errorf("event volume copied/skipped = %d/%d; want 0/>0", ev.BytesCopied, ev.BytesSkipped)
	}
}
