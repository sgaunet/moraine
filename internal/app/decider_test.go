package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/app"
	"github.com/sgaunet/moraine/internal/classify"
	"github.com/sgaunet/moraine/internal/config"
)

// counter counts requests across a fake's handler goroutines.
type counter struct {
	mu sync.Mutex
	n  int
}

func (c *counter) inc() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return c.n
}

func (c *counter) get() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// describingOllama answers describe requests with a sentence and single-call
// classifications with "mountain", counting the chat requests that are not the
// warm-up.
func describingOllama(t *testing.T, chats *counter) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(`{"models":[{"name":"` + stubModel + `"}]}`))
			return
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if string(body["messages"]) == "[]" {
			_, _ = w.Write([]byte(`{"message":{"content":""}}`)) // warm-up
			return
		}
		chats.inc()
		if _, classifying := body["format"]; classifying {
			_, _ = w.Write([]byte(`{"message":{"content":"mountain"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"message":{"content":"A few people around a table."}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// systemone is a fake decision service. The readiness check is its first request;
// every later one is answered by answer(n), n counting decisions from 1.
func systemone(t *testing.T, requests *counter, answer func(n int, w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		n := requests.inc()
		if n == 1 {
			writeChoice(w, "other")
			return
		}
		answer(n-1, w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func writeChoice(w http.ResponseWriter, choice string) {
	_, _ = w.Write([]byte(`{"answers":{"theme":{"type":"choice","choice":"` + choice +
		`","answer_confidence":0.8}}}`))
}

func deciderCfg(src, dest, ollama, decider string) config.Config {
	cfg := modelCfg(src, dest, ollama)
	cfg.Sample = 3
	cfg.Decider = config.DeciderLaya
	cfg.DeciderURL = decider
	cfg.DeciderModel = config.DefaultLayaModel
	return cfg
}

func debugLogger(buf *safeBuffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestOrganizeWithADeciderReportsDescribed(t *testing.T) {
	src, dest := eventSource(t, 2), t.TempDir()
	var chats, decisions counter
	ollama := describingOllama(t, &chats)
	svc := systemone(t, &decisions, func(n int, w http.ResponseWriter, _ *http.Request) {
		writeChoice(w, map[int]string{1: "nature", 2: "family"}[n])
	})

	logs := &safeBuffer{}
	sum, err := app.Organize(context.Background(), deciderCfg(src, dest, ollama.URL, svc.URL), debugLogger(logs), nil, nil)
	if err != nil {
		t.Fatalf("Organize: %v", err)
	}
	got := make([]string, 0, len(sum.Events))
	for _, ev := range sum.Events {
		if ev.Method != string(classify.MethodDescribed) {
			t.Errorf("event method = %q; want described", ev.Method)
		}
		got = append(got, ev.Theme)
	}
	if !reflect.DeepEqual(got, []string{"nature", "family"}) {
		t.Errorf("themes = %v; want the decision service's nature, family", got)
	}
	if n := strings.Count(logs.String(), "method=described"); n != 2 {
		t.Errorf("group lines with method=described = %d; want 2:\n%s", n, logs.String())
	}
	if sum.Copied != 2 {
		t.Errorf("copied = %d; want 2", sum.Copied)
	}

	// An incremental re-run over the placed events asks neither service anything.
	chatsBefore, decisionsBefore := chats.get(), decisions.get()
	inc := deciderCfg(src, dest, ollama.URL, svc.URL)
	inc.Incremental = true
	if _, err := app.Organize(context.Background(), inc, quietLogger(), nil, nil); err != nil {
		t.Fatalf("incremental Organize: %v", err)
	}
	if chats.get() != chatsBefore || decisions.get() != decisionsBefore {
		t.Errorf("incremental run made %d chat and %d decision requests; want none",
			chats.get()-chatsBefore, decisions.get()-decisionsBefore)
	}
}

func TestOrganizeDryRunWithADeciderWritesNothing(t *testing.T) {
	src, dest := eventSource(t, 1), t.TempDir()
	var chats, decisions counter
	ollama := describingOllama(t, &chats)
	svc := systemone(t, &decisions, func(_ int, w http.ResponseWriter, _ *http.Request) { writeChoice(w, "family") })
	cfg := deciderCfg(src, dest, ollama.URL, svc.URL)
	cfg.DryRun = true
	sum, err := app.Organize(context.Background(), cfg, quietLogger(), nil, nil)
	if err != nil {
		t.Fatalf("Organize: %v", err)
	}
	if sum.Events[0].Method != string(classify.MethodDescribed) {
		t.Errorf("method = %q; want described", sum.Events[0].Method)
	}
	if entries, _ := os.ReadDir(dest); len(entries) != 0 {
		t.Errorf("a dry run wrote %d entries", len(entries))
	}
}

// withoutEvents drops the per-event methods, which are the one thing a degraded run
// may legitimately report differently.
func withoutEvents(s app.Summary) app.Summary {
	s.Events = nil
	return s
}

// TestOrganizeDegradesWhenTheDeciderIsUnusable is SC-003: an unreachable or refusing
// decision service costs one warning and nothing else — every photo is placed, and
// the summary is the one a run without the feature produces.
func TestOrganizeDegradesWhenTheDeciderIsUnusable(t *testing.T) {
	const key = "k3y-must-not-appear"
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"invalid or missing bearer token"}`))
	}))
	t.Cleanup(refusing.Close)

	for _, tc := range []struct{ name, url string }{{"unreachable", closedURL}, {"401", refusing.URL}} {
		t.Run(tc.name, func(t *testing.T) {
			src := eventSource(t, 3)
			var chats counter
			ollama := describingOllama(t, &chats)

			baseline, err := app.Organize(context.Background(), modelCfgSample(src, t.TempDir(), ollama.URL), quietLogger(), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			cfg := deciderCfg(src, t.TempDir(), ollama.URL, tc.url)
			cfg.DeciderAPIKey = key
			logs := &safeBuffer{}
			sum, err := app.Organize(context.Background(), cfg, debugLogger(logs), nil, nil)
			if err != nil {
				t.Fatalf("Organize: %v", err)
			}
			if !reflect.DeepEqual(withoutEvents(sum), withoutEvents(baseline)) {
				t.Errorf("summary = %+v; want the no-decider run's %+v", withoutEvents(sum), withoutEvents(baseline))
			}
			for _, ev := range sum.Events {
				if ev.Method != string(classify.MethodModelAll) {
					t.Errorf("method = %q; want model-all", ev.Method)
				}
			}
			if n := strings.Count(logs.String(), "decision service unusable"); n != 1 {
				t.Errorf("unusable warnings = %d; want 1:\n%s", n, logs.String())
			}
			if strings.Contains(logs.String(), key) {
				t.Error("the logs carry the API key")
			}
		})
	}
}

func modelCfgSample(src, dest, url string) config.Config {
	cfg := modelCfg(src, dest, url)
	cfg.Sample = 3
	return cfg
}

// TestOrganizeInterruptDuringADecision aims the interrupt at the describe-then-decide
// step: the run returns promptly with the interrupt, places nothing it had not
// placed, and records no decision failure.
func TestOrganizeInterruptDuringADecision(t *testing.T) {
	src, dest := eventSource(t, 2), t.TempDir()
	var chats, decisions counter
	ollama := describingOllama(t, &chats)
	svc := systemone(t, &decisions, func(_ int, _ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // a decision in flight never answers
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := &safeBuffer{}
	logger := slog.New(&cancelOnMessage{
		msg: "description", cancel: cancel,
		inner: slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}),
	})

	start := time.Now()
	sum, err := app.Organize(ctx, deciderCfg(src, dest, ollama.URL, svc.URL), logger, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v; want the interrupt", err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("the interrupt took %s to land", took)
	}
	if sum.Copied != 0 || sum.Errors != 0 || sum.Scanned != 2 {
		t.Errorf("summary = %+v; want the scan counted and nothing placed or failed", sum)
	}
	if strings.Contains(logs.String(), "decision failed") || strings.Contains(logs.String(), "unusable") {
		t.Errorf("an interrupt was reported as a decision failure:\n%s", logs.String())
	}
}

// Past about 20 options Laya trims every description silently, so a run says so once.
func TestOrganizeWarnsAboutManyThemesForLaya(t *testing.T) {
	src, dest := eventSource(t, 1), t.TempDir()
	var chats, decisions counter
	ollama := describingOllama(t, &chats)
	svc := systemone(t, &decisions, func(_ int, w http.ResponseWriter, _ *http.Request) { writeChoice(w, "t0") })
	for _, tc := range []struct {
		themes int
		want   int
	}{{19, 0}, {20, 1}} {
		cfg := deciderCfg(src, dest, ollama.URL, svc.URL)
		cfg.DryRun = true
		cfg.Themes = nil
		for i := range tc.themes {
			cfg.Themes = append(cfg.Themes, "t"+strconv.Itoa(i))
		}
		logs := &safeBuffer{}
		if _, err := app.Organize(context.Background(), cfg, debugLogger(logs), nil, nil); err != nil {
			t.Fatalf("Organize: %v", err)
		}
		if n := strings.Count(logs.String(), "many themes for the decision service"); n != tc.want {
			t.Errorf("%d themes: %d warnings; want %d", tc.themes, n, tc.want)
		}
	}
}
