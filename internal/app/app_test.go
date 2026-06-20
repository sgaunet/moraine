package app_test

import (
	"bytes"
	"context"
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
	if !strings.Contains(buf.String(), "fichier ignoré") {
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
	for _, want := range []string{"groupe", "methode", "theme", "photo", "action=copied", "dest=", "résumé"} {
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
	cfg.Model = "qwen2.5vl:7b"
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
	if !strings.Contains(buf.String(), "Ollama injoignable") {
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
	cfg.Model = "qwen2.5vl:7b"
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
	if !strings.Contains(out, "ollama pull qwen2.5vl:7b") {
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
	if strings.Contains(buf.String(), "groupe") {
		t.Errorf("warn level must suppress info lines, got:\n%s", buf.String())
	}
}
