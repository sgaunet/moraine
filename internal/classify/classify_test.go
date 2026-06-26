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

var themes = []string{"family", "mountain", "special-events", "nature"}

func opts(c classify.Classifier) classify.Options {
	return classify.Options{Themes: themes, Fallback: "other", Classifier: c}
}

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

func TestHeuristicMountainInSet(t *testing.T) {
	c := photo.Cluster{Start: time.Now(), Photos: []photo.Photo{{Altitude: ptr(2400)}}}
	theme, method := classify.Label(context.Background(), c, opts(nil))
	if theme != "mountain" || method != classify.MethodHeuristic {
		t.Fatalf("got (%q,%q); want (mountain,heuristic)", theme, method)
	}
}

func TestHeuristicMountainNotInSetFallsThrough(t *testing.T) {
	c := photo.Cluster{Start: time.Now(), Photos: []photo.Photo{{Altitude: ptr(2400)}}}
	o := classify.Options{Themes: []string{"family", "nature"}, Fallback: "other"}
	theme, method := classify.Label(context.Background(), c, o)
	if theme != "other" || method != classify.MethodFallback {
		t.Fatalf("got (%q,%q); want (other,fallback)", theme, method)
	}
}

func TestFallbackWhenNoClassifier(t *testing.T) {
	c := photo.Cluster{Start: time.Now(), Photos: []photo.Photo{{}}}
	theme, method := classify.Label(context.Background(), c, opts(nil))
	if theme != "other" || method != classify.MethodFallback {
		t.Fatalf("got (%q,%q); want (other,fallback)", theme, method)
	}
}

func TestClassifierInSetSmallGroupIsModelAll(t *testing.T) {
	fc := &fakeClassifier{label: "nature"}
	c := photo.Cluster{Photos: []photo.Photo{{}, {}, {}}} // 3 → all
	theme, method := classify.Label(context.Background(), c, opts(fc))
	if theme != "nature" || method != classify.MethodModelAll {
		t.Fatalf("got (%q,%q); want (nature,model-all)", theme, method)
	}
	if fc.calls != 1 {
		t.Errorf("classifier calls = %d; want 1", fc.calls)
	}
}

func TestClassifierInSetLargeGroupIsModelSample(t *testing.T) {
	fc := &fakeClassifier{label: "nature"}
	c := photo.Cluster{Photos: []photo.Photo{{}, {}, {}, {}, {}}} // 5 → sample
	theme, method := classify.Label(context.Background(), c, opts(fc))
	if theme != "nature" || method != classify.MethodModelSample {
		t.Fatalf("got (%q,%q); want (nature,model-sample)", theme, method)
	}
}

func TestClassifierOutOfSetFallsBack(t *testing.T) {
	fc := &fakeClassifier{label: "concert"} // not a configured theme
	c := photo.Cluster{Photos: []photo.Photo{{}}}
	theme, method := classify.Label(context.Background(), c, opts(fc))
	if theme != "other" || method != classify.MethodFallback {
		t.Fatalf("got (%q,%q); want (other,fallback)", theme, method)
	}
}

func TestClassifierErrorFallsBack(t *testing.T) {
	fc := &fakeClassifier{err: errors.New("ollama down")}
	c := photo.Cluster{Photos: []photo.Photo{{}}}
	theme, method := classify.Label(context.Background(), c, opts(fc))
	if theme != "other" || method != classify.MethodFallback {
		t.Fatalf("got (%q,%q); want (other,fallback)", theme, method)
	}
}

func TestModelWinsOverAltitude(t *testing.T) {
	// A high-altitude cluster the model recognises as e.g. family must NOT be
	// hard-overridden to "mountain": the model decides when it is available.
	fc := &fakeClassifier{label: "family"}
	c := photo.Cluster{Photos: []photo.Photo{{Altitude: ptr(2400)}}}
	theme, method := classify.Label(context.Background(), c, opts(fc))
	if theme != "family" || method != classify.MethodModelAll {
		t.Fatalf("got (%q,%q); want (family,model-all)", theme, method)
	}
}

func TestAltitudeFallbackWhenModelFails(t *testing.T) {
	// When the model errors (or abstains), the altitude heuristic still applies
	// so an offline / unavailable run keeps classifying obvious mountain photos.
	fc := &fakeClassifier{err: errors.New("ollama down")}
	c := photo.Cluster{Photos: []photo.Photo{{Altitude: ptr(2400)}}}
	theme, method := classify.Label(context.Background(), c, opts(fc))
	if theme != "mountain" || method != classify.MethodHeuristic {
		t.Fatalf("got (%q,%q); want (mountain,heuristic)", theme, method)
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

func jpegCluster(t *testing.T, n int) photo.Cluster {
	t.Helper()
	dir := t.TempDir()
	var ps []photo.Photo
	for i := 0; i < n; i++ {
		p := filepath.Join(dir, "p"+string(rune('0'+i))+".jpg")
		writeTinyJPEG(t, p)
		ps = append(ps, photo.Photo{Path: p, Name: filepath.Base(p), Format: photo.JPEG})
	}
	return photo.Cluster{Photos: ps}
}

func TestOllamaClassifyMapsInSetTheme(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path = %q; want /api/chat", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"Mountain.\n"}}`))
	}))
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "qwen3-vl:8b", 1, themes)
	got, err := oc.Classify(context.Background(), jpegCluster(t, 1))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != "mountain" {
		t.Fatalf("theme = %q; want normalised 'mountain'", got)
	}
}

func TestOllamaClassifyParsesStructuredJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"{\"category\":\"mountain\"}"}}`))
	}))
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "qwen3-vl:8b", 1, themes)
	got, err := oc.Classify(context.Background(), jpegCluster(t, 1))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != "mountain" {
		t.Fatalf("theme = %q; want 'mountain' from structured answer", got)
	}
}

func TestOllamaClassifyOutOfSetIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"message":{"content":"concert"}}`))
	}))
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 1, themes)
	if _, err := oc.Classify(context.Background(), jpegCluster(t, 1)); err == nil {
		t.Fatal("expected error for out-of-set answer")
	}
}

func TestOllamaClassifyRetriesThenFails(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 1, themes)
	if _, err := oc.Classify(context.Background(), jpegCluster(t, 1)); err == nil {
		t.Fatal("expected error when Ollama returns 500")
	}
	if calls < 2 {
		t.Errorf("server called %d times; want at least 2 (one retry)", calls)
	}
}

func TestOllamaClassifyTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"message":{"content":"mountain"}}`))
	}))
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 1, themes)
	oc.Timeout = 20 * time.Millisecond
	if _, err := oc.Classify(context.Background(), jpegCluster(t, 1)); err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestOllamaClassifyHEICOnlyNoDecodableImage(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"message":{"content":"mountain"}}`))
	}))
	defer srv.Close()

	c := photo.Cluster{Photos: []photo.Photo{{Path: "x.heic", Format: photo.HEIC}}}
	oc := classify.NewOllama(srv.URL, "m", 3, themes)
	if _, err := oc.Classify(context.Background(), c); err == nil {
		t.Fatal("expected error: no decodable image to classify")
	}
	if calls != 0 {
		t.Errorf("server called %d times; want 0 (HEIC not sent)", calls)
	}
}
