package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/app"
	"github.com/sgaunet/moraine/internal/config"
	"github.com/sgaunet/moraine/internal/exiftooltest"
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

var modTime = time.Date(2025, 8, 12, 12, 0, 0, 0, time.Local)

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

	sum, err := app.Organize(context.Background(), cfg, quietLogger())
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
	sum2, err := app.Organize(context.Background(), cfg, quietLogger())
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

	sum, err := app.Organize(context.Background(), cfg, quietLogger())
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

	sum, err := app.Organize(context.Background(), baseCfg(src, dest, true), quietLogger())
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
	sum, err := app.Organize(context.Background(), baseCfg(src, dest, true), logger)
	if err != nil {
		t.Fatalf("run must not abort on an unreadable file: %v", err)
	}
	if sum.Copied != 1 {
		t.Fatalf("Copied = %d; want 1 (unreadable skipped)", sum.Copied)
	}
	if !strings.Contains(buf.String(), "file skipped") {
		t.Error("expected a warning log for the unreadable file")
	}
}

func TestOrganizeLoggingContract(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	makePNG(t, filepath.Join(src, "a.png"))

	buf := &safeBuffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if _, err := app.Organize(context.Background(), baseCfg(src, dest, true), logger); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"group", "method", "theme", "photo", "action=copied", "dest=", "summary"} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q\n---\n%s", want, out)
		}
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
	cfg.Model = "qwen3-vl:8b"
	cfg.OllamaURL = url

	buf := &safeBuffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	sum, err := app.Organize(context.Background(), cfg, logger)
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
	cfg.Model = "qwen3-vl:8b"
	cfg.OllamaURL = srv.URL

	buf := &safeBuffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	sum, err := app.Organize(context.Background(), cfg, logger)
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
	if _, err := app.Organize(context.Background(), baseCfg(src, dest, true), logger); err != nil {
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

// ollamaStub serves /api/tags (advertising model) and /api/chat (always
// answering "mountain"), invoking onChat with the number of images received.
func ollamaStub(t *testing.T, model string, onChat func(images int)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(`{"models":[{"name":"` + model + `"}]}`))
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
	sum, err := app.Organize(context.Background(), baseCfg(src, dest, true), quietLogger())
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

	sum, err := app.Organize(context.Background(), baseCfg(file, dest, false), quietLogger())
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
	srv := ollamaStub(t, "qwen3-vl:8b", func(n int) { gotImages = n })
	defer srv.Close()

	cfg := baseCfg(src, dest, true)
	cfg.Sample = 3
	cfg.Model = "qwen3-vl:8b"
	cfg.OllamaURL = srv.URL
	cfg.ExifToolPath = exifPath

	sum, err := app.Organize(context.Background(), cfg, quietLogger())
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
	srv := ollamaStub(t, "qwen3-vl:8b", nil)
	defer srv.Close()

	cfg := baseCfg(src, dest, true)
	cfg.Sample = 3
	cfg.Model = "qwen3-vl:8b"
	cfg.OllamaURL = srv.URL
	cfg.ExifToolPath = exifPath

	// Redirect TMPDIR only after all helper temp dirs/stubs exist, so a stray
	// preview temp write (there should be none) would land in the monitored dir.
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	if _, err := app.Organize(context.Background(), cfg, quietLogger()); err != nil {
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
