package classify_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/sgaunet/moraine/internal/classify"
	"github.com/sgaunet/moraine/internal/photo"
	"github.com/sgaunet/moraine/internal/rawpreview"
)

// fakeExtractor is an in-memory classify.RawExtractor for tests (no exiftool).
type fakeExtractor struct {
	data []byte
	err  error
}

func (f fakeExtractor) Extract(context.Context, string) ([]byte, error) {
	return f.data, f.err
}

func rawCluster(n int) photo.Cluster {
	var ps []photo.Photo
	for i := 0; i < n; i++ {
		ps = append(ps, photo.Photo{Path: fmt.Sprintf("r%d.dng", i), Format: photo.RAW})
	}
	return photo.Cluster{Photos: ps}
}

func TestOllamaClassifyRAWUsesExtractor(t *testing.T) {
	var gotImages int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Images []string `json:"images"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		for _, m := range body.Messages {
			gotImages += len(m.Images)
		}
		_, _ = w.Write([]byte(`{"message":{"content":"mountain"}}`))
	}))
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 3, themes)
	oc.Raw = fakeExtractor{data: []byte("PREVIEWBYTES")}
	got, err := oc.Classify(context.Background(), rawCluster(1))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != "mountain" {
		t.Errorf("theme = %q; want mountain", got)
	}
	if gotImages < 1 {
		t.Errorf("model received %d images; want ≥1 (RAW preview must be sent)", gotImages)
	}
}

func TestOllamaClassifyRAWNoPreviewIsError(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"message":{"content":"mountain"}}`))
	}))
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 3, themes)
	oc.Raw = fakeExtractor{err: rawpreview.ErrNoPreview}
	if _, err := oc.Classify(context.Background(), rawCluster(1)); err == nil {
		t.Fatal("expected error: a RAW with no preview yields no usable image")
	}
	if calls != 0 {
		t.Errorf("server called %d times; want 0 (no image to send)", calls)
	}
}

// safeBuffer is a concurrency-safe slog sink.
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

func tagsServer(t *testing.T, names ...string) *httptest.Server {
	t.Helper()
	var quoted []string
	for _, n := range names {
		quoted = append(quoted, `{"name":"`+n+`"}`)
	}
	body := `{"models":[` + strings.Join(quoted, ",") + `]}`
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("path = %q; want /api/tags", r.URL.Path)
		}
		_, _ = w.Write([]byte(body))
	}))
}

func TestPreflightReady(t *testing.T) {
	srv := tagsServer(t, "qwen3-vl:8b", "llama3:8b")
	defer srv.Close()
	oc := classify.NewOllama(srv.URL, "qwen3-vl:8b", 3, themes)
	if got := oc.Preflight(context.Background()); got != classify.StatusReady {
		t.Fatalf("got %v; want StatusReady", got)
	}
}

func TestPreflightReadyUntaggedConfig(t *testing.T) {
	srv := tagsServer(t, "qwen3-vl:8b")
	defer srv.Close()
	oc := classify.NewOllama(srv.URL, "qwen3-vl", 3, themes) // no tag → match by base
	if got := oc.Preflight(context.Background()); got != classify.StatusReady {
		t.Fatalf("got %v; want StatusReady", got)
	}
}

func TestPreflightModelMissing(t *testing.T) {
	srv := tagsServer(t, "llama3:8b")
	defer srv.Close()
	oc := classify.NewOllama(srv.URL, "qwen3-vl:8b", 3, themes)
	if got := oc.Preflight(context.Background()); got != classify.StatusModelMissing {
		t.Fatalf("got %v; want StatusModelMissing", got)
	}
}

func TestPreflightUnreachable(t *testing.T) {
	srv := tagsServer(t)
	srv.Close() // closed → connection refused
	oc := classify.NewOllama(srv.URL, "qwen3-vl:8b", 3, themes)
	if got := oc.Preflight(context.Background()); got != classify.StatusUnreachable {
		t.Fatalf("got %v; want StatusUnreachable", got)
	}
}

func TestPromptListsEveryTheme(t *testing.T) {
	// The prompt must give the model the full category list (user requirement).
	// Instructions now span a system + user message, so check across both.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		var prompt string
		for _, m := range got.Messages {
			prompt += m.Content + "\n"
		}
		for _, theme := range themes {
			if !strings.Contains(prompt, theme) {
				t.Errorf("prompt missing theme %q\nprompt: %s", theme, prompt)
			}
		}
		_, _ = w.Write([]byte(`{"message":{"content":"nature"}}`))
	}))
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 1, themes)
	if _, err := oc.Classify(context.Background(), jpegCluster(t, 1)); err != nil {
		t.Fatalf("Classify: %v", err)
	}
}

func TestClassifyRequestCarriesEnumSchema(t *testing.T) {
	// The request must use a system + user message and constrain the answer with
	// a JSON Schema enum equal to the configured themes.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
			Format struct {
				Properties struct {
					Category struct {
						Enum []string `json:"enum"`
					} `json:"category"`
				} `json:"properties"`
			} `json:"format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		roles := []string{}
		for _, m := range got.Messages {
			roles = append(roles, m.Role)
		}
		if len(roles) != 2 || roles[0] != "system" || roles[1] != "user" {
			t.Errorf("message roles = %v; want [system user]", roles)
		}
		if got.Format.Properties.Category.Enum == nil {
			t.Fatalf("request carried no format.properties.category.enum")
		}
		if !equalStrings(got.Format.Properties.Category.Enum, themes) {
			t.Errorf("enum = %v; want %v", got.Format.Properties.Category.Enum, themes)
		}
		_, _ = w.Write([]byte(`{"message":{"content":"{\"category\":\"nature\"}"}}`))
	}))
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 1, themes)
	if _, err := oc.Classify(context.Background(), jpegCluster(t, 1)); err != nil {
		t.Fatalf("Classify: %v", err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestClassifyDebugLogsAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"message":{"content":"nature"}}`)) // valid, in-set
	}))
	defer srv.Close()

	buf := &safeBuffer{}
	oc := classify.NewOllama(srv.URL, "m", 1, themes)
	oc.Logger = slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if _, err := oc.Classify(context.Background(), jpegCluster(t, 1)); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "model answer") || !strings.Contains(out, "nature") {
		t.Errorf("expected a debug log naming the model answer 'nature', got:\n%s", out)
	}
}

func TestClassifyAnswerNotLoggedAtInfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"message":{"content":"nature"}}`)) // valid, in-set
	}))
	defer srv.Close()

	buf := &safeBuffer{}
	oc := classify.NewOllama(srv.URL, "m", 1, themes)
	oc.Logger = slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if _, err := oc.Classify(context.Background(), jpegCluster(t, 1)); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if out := buf.String(); strings.Contains(out, "model answer") {
		t.Errorf("did not expect the model answer log at info level, got:\n%s", out)
	}
}

func TestClassifyLogsRejectedAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"message":{"content":"beach"}}`)) // not in the set
	}))
	defer srv.Close()

	buf := &safeBuffer{}
	oc := classify.NewOllama(srv.URL, "m", 1, themes)
	oc.Logger = slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if _, err := oc.Classify(context.Background(), jpegCluster(t, 1)); err == nil {
		t.Fatal("expected error for out-of-set answer")
	}
	out := buf.String()
	if !strings.Contains(out, "fallback") || !strings.Contains(out, "beach") {
		t.Errorf("expected a warn log naming the rejected answer 'beach', got:\n%s", out)
	}
}
