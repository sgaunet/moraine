package classify_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/classify"
	"github.com/sgaunet/moraine/internal/photo"
	"github.com/sgaunet/moraine/internal/rawpreview"
)

// chatOnly wraps a handler that asserts on a classification request, answering
// the run's one-off warm-up itself. A warm-up is a POST to /api/chat carrying no
// messages — it loads the model and asks nothing — so it has neither a prompt nor
// a schema for those assertions to inspect.
func chatOnly(t *testing.T, next http.HandlerFunc) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var probe struct {
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal(body, &probe); err == nil && len(probe.Messages) == 0 {
			_, _ = w.Write([]byte(`{"message":{"content":""}}`)) // warm-up: model loaded
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		next(w, r)
	}
}

// fakeExtractor is an in-memory classify.PreviewExtractor for tests (no exiftool).
type fakeExtractor struct {
	data []byte
	err  error
}

func (f fakeExtractor) Extract(context.Context, string) ([]byte, error) {
	return f.data, f.err
}

func rawCluster(n int) photo.Cluster {
	ps := make([]photo.Photo, 0, n)
	for i := range n {
		ps = append(ps, photo.Photo{Path: fmt.Sprintf("r%d.dng", i), Format: photo.RAW})
	}
	return photo.Cluster{Photos: ps}
}

func TestOllamaClassifyRAWUsesExtractor(t *testing.T) {
	var gotImages int
	srv := httptest.NewServer(chatOnly(t, func(w http.ResponseWriter, r *http.Request) {
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
	oc.RawPreview = fakeExtractor{data: []byte("PREVIEWBYTES")}
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
	oc.RawPreview = fakeExtractor{err: rawpreview.ErrNoPreview}
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
	quoted := make([]string, 0, len(names))
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
	srv := httptest.NewServer(chatOnly(t, func(w http.ResponseWriter, r *http.Request) {
		var got struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for _, m := range got.Messages {
			b.WriteString(m.Content)
			b.WriteString("\n")
		}
		prompt := b.String()
		for _, theme := range themes {
			if !strings.Contains(prompt, theme) {
				t.Errorf("prompt missing theme %q\nprompt: %s", theme, prompt)
			}
		}
		_, _ = w.Write([]byte(`{"message":{"content":"nature"}}`))
	}))
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 1, themes)
	if _, err := oc.Classify(context.Background(), jpegCluster(t)); err != nil {
		t.Fatalf("Classify: %v", err)
	}
}

func TestClassifyRequestCarriesEnumSchema(t *testing.T) {
	// The request must use a system + user message and constrain the answer with
	// a JSON Schema enum equal to the configured themes plus the "none" abstain
	// sentinel (so the model can decline rather than be forced to pick a theme).
	wantEnum := append(append([]string{}, themes...), "none")
	srv := httptest.NewServer(chatOnly(t, func(w http.ResponseWriter, r *http.Request) {
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
		roles := make([]string, 0, len(got.Messages))
		for _, m := range got.Messages {
			roles = append(roles, m.Role)
		}
		if len(roles) != 2 || roles[0] != "system" || roles[1] != "user" {
			t.Errorf("message roles = %v; want [system user]", roles)
		}
		if got.Format.Properties.Category.Enum == nil {
			t.Fatalf("request carried no format.properties.category.enum")
		}
		if !equalStrings(got.Format.Properties.Category.Enum, wantEnum) {
			t.Errorf("enum = %v; want %v", got.Format.Properties.Category.Enum, wantEnum)
		}
		_, _ = w.Write([]byte(`{"message":{"content":"{\"category\":\"nature\"}"}}`))
	}))
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 1, themes)
	if _, err := oc.Classify(context.Background(), jpegCluster(t)); err != nil {
		t.Fatalf("Classify: %v", err)
	}
}

func TestClassifyRequestIsDeterministic(t *testing.T) {
	// Every request must pin decoding (temperature 0 + fixed seed) so the same
	// cluster classifies identically on re-runs.
	srv := httptest.NewServer(chatOnly(t, func(w http.ResponseWriter, r *http.Request) {
		var got struct {
			Options struct {
				Temperature float64 `json:"temperature"`
				Seed        int     `json:"seed"`
			} `json:"options"`
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if got.Options.Temperature != 0 {
			t.Errorf("options.temperature = %v; want 0", got.Options.Temperature)
		}
		if got.Options.Seed != 42 {
			t.Errorf("options.seed = %d; want 42", got.Options.Seed)
		}
		_, _ = w.Write([]byte(`{"message":{"content":"nature"}}`))
	}))
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 1, themes)
	if _, err := oc.Classify(context.Background(), jpegCluster(t)); err != nil {
		t.Fatalf("Classify: %v", err)
	}
}

func TestClassifyAbstainReturnsEmptyNoError(t *testing.T) {
	// "none" is an intentional abstention, not a failure: Classify returns
	// ("", nil) and does not retry, so the caller falls back cleanly.
	var calls int
	srv := httptest.NewServer(chatOnly(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"message":{"content":"{\"category\":\"none\"}"}}`))
	}))
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 1, themes)
	got, err := oc.Classify(context.Background(), jpegCluster(t))
	if err != nil {
		t.Fatalf("abstain must not error: %v", err)
	}
	if got != "" {
		t.Errorf("theme = %q; want \"\" on abstain", got)
	}
	if calls != 1 {
		t.Errorf("server called %d times; want 1 (abstain is not retried)", calls)
	}
}

func TestPromptDescribesThemes(t *testing.T) {
	// The prompt must describe each built-in theme, not pass a bare slug, so the
	// vision model has something concrete to match.
	describable := []string{"cook", "mountain"}
	srv := httptest.NewServer(chatOnly(t, func(w http.ResponseWriter, r *http.Request) {
		var got struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for _, m := range got.Messages {
			b.WriteString(m.Content)
			b.WriteString("\n")
		}
		prompt := b.String()
		for _, want := range []string{"cooking", "mountains"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("prompt missing description %q\nprompt: %s", want, prompt)
			}
		}
		_, _ = w.Write([]byte(`{"message":{"content":"cook"}}`))
	}))
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 1, describable)
	if _, err := oc.Classify(context.Background(), jpegCluster(t)); err != nil {
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
	if _, err := oc.Classify(context.Background(), jpegCluster(t)); err != nil {
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
	if _, err := oc.Classify(context.Background(), jpegCluster(t)); err != nil {
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
	if _, err := oc.Classify(context.Background(), jpegCluster(t)); err == nil {
		t.Fatal("expected error for out-of-set answer")
	}
	out := buf.String()
	if !strings.Contains(out, "fallback") || !strings.Contains(out, "beach") {
		t.Errorf("expected a warn log naming the rejected answer 'beach', got:\n%s", out)
	}
}

func TestWarmupLoadsModelWithoutAsking(t *testing.T) {
	// A warm-up is a POST to /api/chat with no messages: Ollama loads the model,
	// holds it for keep_alive, and answers nothing.
	var (
		calls     int
		path      string
		messages  int
		keepAlive string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		path = r.URL.Path
		var body struct {
			Messages  []json.RawMessage `json:"messages"`
			KeepAlive string            `json:"keep_alive"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		messages, keepAlive = len(body.Messages), body.KeepAlive
		_, _ = w.Write([]byte(`{"message":{"content":""}}`))
	}))
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 1, themes)
	if err := oc.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup: %v", err)
	}
	if calls != 1 || path != "/api/chat" {
		t.Errorf("warm-up made %d call(s) to %q; want 1 to /api/chat", calls, path)
	}
	if messages != 0 {
		t.Errorf("warm-up carried %d messages; want 0 (it asks nothing)", messages)
	}
	if keepAlive != classify.DefaultKeepAlive {
		t.Errorf("warm-up keep_alive = %q; want %q", keepAlive, classify.DefaultKeepAlive)
	}
}

func TestClassifyWarmsUpOnceAndKeepsTheModelAlive(t *testing.T) {
	// The cold load is paid once per classifier, not once per cluster, and every
	// classification asks Ollama to keep the model resident.
	var warmups, chats int
	var keepAlive string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages  []json.RawMessage `json:"messages"`
			KeepAlive string            `json:"keep_alive"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Messages) == 0 {
			warmups++
		} else {
			chats++
			keepAlive = body.KeepAlive
		}
		_, _ = w.Write([]byte(`{"message":{"content":"mountain"}}`))
	}))
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 1, themes)
	for range 3 {
		if _, err := oc.Classify(context.Background(), jpegCluster(t)); err != nil {
			t.Fatalf("Classify: %v", err)
		}
	}
	if warmups != 1 {
		t.Errorf("warm-ups = %d; want exactly 1 for three classifications", warmups)
	}
	if chats != 3 {
		t.Errorf("classifications = %d; want 3", chats)
	}
	if keepAlive != classify.DefaultKeepAlive {
		t.Errorf("classification keep_alive = %q; want %q", keepAlive, classify.DefaultKeepAlive)
	}
}

func TestClassifyDoesNotRetryRejectedAnswer(t *testing.T) {
	// Decoding is pinned (temperature 0, fixed seed), so re-asking after an
	// out-of-set answer would only re-read the same answer.
	var calls int
	srv := httptest.NewServer(chatOnly(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"message":{"content":"beach"}}`)) // not in the set
	}))
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 1, themes)
	if _, err := oc.Classify(context.Background(), jpegCluster(t)); err == nil {
		t.Fatal("expected an error for an out-of-set answer")
	}
	if calls != 1 {
		t.Errorf("server called %d times; want 1 (a rejected answer is deterministic)", calls)
	}
}

func TestClassifyDoesNotRetryClientError(t *testing.T) {
	// A 4xx says the request itself is wrong; repeating it verbatim cannot help.
	var calls int
	srv := httptest.NewServer(chatOnly(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 1, themes)
	if _, err := oc.Classify(context.Background(), jpegCluster(t)); err == nil {
		t.Fatal("expected an error for a 400 response")
	}
	if calls != 1 {
		t.Errorf("server called %d times; want 1 (a client error is not transient)", calls)
	}
}

func TestClassifyRetriesTransientThenSucceeds(t *testing.T) {
	// 503 is what Ollama answers while it is still loading: exactly the failure a
	// retry is for.
	var calls int
	srv := httptest.NewServer(chatOnly(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"message":{"content":"mountain"}}`))
	}))
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 1, themes)
	got, err := oc.Classify(context.Background(), jpegCluster(t))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != "mountain" {
		t.Errorf("theme = %q; want mountain after one transient failure", got)
	}
	if calls != 2 {
		t.Errorf("server called %d times; want 2 (one failure, one retry)", calls)
	}
}

func TestPromptCarriesEXIFContext(t *testing.T) {
	// The model sees pixels only; altitude, time and place are already known and
	// cost a handful of tokens to pass along.
	dir := t.TempDir()
	file := filepath.Join(dir, "p0.jpg")
	writeTinyJPEG(t, file)

	start := time.Date(2025, 7, 14, 8, 32, 0, 0, time.UTC)
	altitude := 2410.0
	c := photo.Cluster{
		Start: start,
		End:   start.Add(3*time.Hour + 38*time.Minute),
		Photos: []photo.Photo{{
			Path:     file,
			Name:     "p0.jpg",
			Format:   photo.JPEG,
			Altitude: &altitude,
			GPS:      &photo.LatLng{Lat: 45.8326, Lng: 6.8652},
		}},
	}

	var prompt string
	srv := httptest.NewServer(chatOnly(t, func(w http.ResponseWriter, r *http.Request) {
		var got struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for _, m := range got.Messages {
			b.WriteString(m.Content)
			b.WriteString("\n")
		}
		prompt = b.String()
		_, _ = w.Write([]byte(`{"message":{"content":"mountain"}}`))
	}))
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 1, themes)
	if _, err := oc.Classify(context.Background(), c); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	for _, want := range []string{"2410 m", "2025-07-14 08:32", "2025-07-14 12:10", "45.83, 6.87"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q\nprompt: %s", want, prompt)
		}
	}
}

func TestPromptCollapsesASingleMinuteEvent(t *testing.T) {
	// A burst spanning seconds renders as one time, not as "11:13 to 11:13".
	dir := t.TempDir()
	file := filepath.Join(dir, "p0.jpg")
	writeTinyJPEG(t, file)

	start := time.Date(2016, 6, 11, 11, 13, 44, 0, time.UTC)
	c := photo.Cluster{
		Start:  start,
		End:    start.Add(7 * time.Second),
		Photos: []photo.Photo{{Path: file, Name: "p0.jpg", Format: photo.JPEG}},
	}

	var prompt string
	srv := httptest.NewServer(chatOnly(t, func(w http.ResponseWriter, r *http.Request) {
		var got struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for _, m := range got.Messages {
			b.WriteString(m.Content)
		}
		prompt = b.String()
		_, _ = w.Write([]byte(`{"message":{"content":"mountain"}}`))
	}))
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 1, themes)
	if _, err := oc.Classify(context.Background(), c); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if want := "taken: 2016-06-11 11:13 (local time)"; !strings.Contains(prompt, want) {
		t.Errorf("prompt missing %q\nprompt: %s", want, prompt)
	}
	if strings.Contains(prompt, "11:13 to 11:13") {
		t.Errorf("prompt renders a zero-width span\nprompt: %s", prompt)
	}
}

func TestPromptOmitsAbsentMetadata(t *testing.T) {
	// A cluster with no EXIF facts must not spend tokens saying so.
	var prompt string
	srv := httptest.NewServer(chatOnly(t, func(w http.ResponseWriter, r *http.Request) {
		var got struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for _, m := range got.Messages {
			b.WriteString(m.Content)
		}
		prompt = b.String()
		_, _ = w.Write([]byte(`{"message":{"content":"mountain"}}`))
	}))
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 1, themes)
	if _, err := oc.Classify(context.Background(), jpegCluster(t)); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	for _, unwanted := range []string{"altitude", "location", "taken:"} {
		if strings.Contains(prompt, unwanted) {
			t.Errorf("prompt mentions %q for a cluster with no such metadata\nprompt: %s", unwanted, prompt)
		}
	}
}
