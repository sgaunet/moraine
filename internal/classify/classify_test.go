package classify_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/classify"
	"github.com/sgaunet/moraine/internal/photo"
)

// fakeClassifier is an in-memory Classifier (no network, no mock framework).
type fakeClassifier struct {
	label string
	err   error
	calls int
}

func (f *fakeClassifier) Classify(_ context.Context, _ photo.Cluster) (string, error) {
	f.calls++
	return f.label, f.err
}

func ptr(f float64) *float64 { return &f }

func TestHeuristicMountainByAltitude(t *testing.T) {
	c := photo.Cluster{
		Start:  time.Now(),
		Photos: []photo.Photo{{Altitude: ptr(2400)}},
	}
	if got := classify.Label(context.Background(), c, classify.Options{}); got != "montagne" {
		t.Fatalf("Label = %q; want montagne", got)
	}
}

func TestHeuristicTravelByDistance(t *testing.T) {
	home := &photo.LatLng{Lat: 45.188, Lng: 5.724} // Grenoble
	c := photo.Cluster{
		Start:  time.Now(),
		Photos: []photo.Photo{{GPS: &photo.LatLng{Lat: 41.902, Lng: 12.496}}}, // Rome (~640 km)
	}
	if got := classify.Label(context.Background(), c, classify.Options{Home: home}); got != "voyage" {
		t.Fatalf("Label = %q; want voyage", got)
	}
}

func TestHeuristicNearHomeIsNotTravel(t *testing.T) {
	home := &photo.LatLng{Lat: 45.188, Lng: 5.724}
	start := time.Date(2025, 8, 12, 0, 0, 0, 0, time.UTC)
	c := photo.Cluster{
		Start:  start,
		Photos: []photo.Photo{{GPS: &photo.LatLng{Lat: 45.19, Lng: 5.73}}}, // ~1 km away
	}
	// No mountain, near home, no classifier → date fallback.
	if got := classify.Label(context.Background(), c, classify.Options{Home: home}); got != "2025-08-12" {
		t.Fatalf("Label = %q; want date fallback 2025-08-12", got)
	}
}

func TestFallbackToDateWhenAllInconclusive(t *testing.T) {
	start := time.Date(2024, 12, 25, 10, 0, 0, 0, time.UTC)
	c := photo.Cluster{Start: start, Photos: []photo.Photo{{Taken: start}}}
	got := classify.Label(context.Background(), c, classify.Options{})
	if got != "2024-12-25" {
		t.Fatalf("Label = %q; want 2024-12-25", got)
	}
}

func TestClassifierUsedWhenHeuristicSilent(t *testing.T) {
	fc := &fakeClassifier{label: "concert"}
	start := time.Date(2024, 12, 25, 10, 0, 0, 0, time.UTC)
	c := photo.Cluster{Start: start, Photos: []photo.Photo{{Taken: start}}}
	got := classify.Label(context.Background(), c, classify.Options{Classifier: fc})
	if got != "concert" {
		t.Fatalf("Label = %q; want concert", got)
	}
	if fc.calls != 1 {
		t.Errorf("classifier called %d times; want 1", fc.calls)
	}
}

func TestClassifierErrorFallsBackToDate(t *testing.T) {
	fc := &fakeClassifier{err: errors.New("ollama down")}
	start := time.Date(2024, 12, 25, 10, 0, 0, 0, time.UTC)
	c := photo.Cluster{Start: start, Photos: []photo.Photo{{Taken: start}}}
	got := classify.Label(context.Background(), c, classify.Options{Classifier: fc})
	if got != "2024-12-25" {
		t.Fatalf("Label = %q; want date fallback on classifier error", got)
	}
}

// ---- Ollama HTTP client (httptest, no real Ollama) -------------------------

func writeTinyJPEG(t *testing.T, path string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestOllamaClassifyParsesCategory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path = %q; want /api/chat", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"Sortie Montagne.\n"}}`))
	}))
	defer srv.Close()

	jpgPath := filepath.Join(t.TempDir(), "p.jpg")
	writeTinyJPEG(t, jpgPath)
	c := photo.Cluster{Photos: []photo.Photo{{Path: jpgPath, Format: photo.JPEG}}}

	oc := classify.NewOllama(srv.URL, "qwen2.5vl:7b", 1)
	got, err := oc.Classify(context.Background(), c)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != "sortie montagne" {
		t.Fatalf("category = %q; want normalised 'sortie montagne'", got)
	}
}

func TestOllamaClassifyRetriesThenFails(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 0)
	_, err := oc.Classify(context.Background(), photo.Cluster{})
	if err == nil {
		t.Fatal("expected error when Ollama returns 500")
	}
	if calls < 2 {
		t.Errorf("server called %d times; want at least 2 (one retry)", calls)
	}
}

func TestOllamaClassifyTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"message":{"content":"x"}}`))
	}))
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 0)
	oc.Timeout = 20 * time.Millisecond
	_, err := oc.Classify(context.Background(), photo.Cluster{})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}
