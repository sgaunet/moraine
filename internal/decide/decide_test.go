package decide_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/sgaunet/moraine/internal/classify"
	"github.com/sgaunet/moraine/internal/decide"
)

// recorded is one request the fake decision service received.
type recorded struct {
	auth string
	body []byte
}

// service is a fake systemone endpoint. answer decides each reply from the request
// ordinal (1-based); it defaults to a Laya-shaped "cook" answer.
type service struct {
	mu       sync.Mutex
	requests []recorded
	answer   func(n int, w http.ResponseWriter)
}

func newService(t *testing.T, answer func(n int, w http.ResponseWriter)) (*service, *httptest.Server) {
	t.Helper()
	s := &service{answer: answer}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" || r.Method != http.MethodPost {
			t.Errorf("request to %s %s; want POST /v1/systemone", r.Method, r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		s.mu.Lock()
		s.requests = append(s.requests, recorded{auth: r.Header.Get("Authorization"), body: body})
		n := len(s.requests)
		s.mu.Unlock()
		if s.answer == nil {
			answerWith(w, "cook", 0.83, 0.4)
			return
		}
		s.answer(n, w)
	}))
	t.Cleanup(srv.Close)
	return s, srv
}

func (s *service) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func (s *service) last(t *testing.T) recorded {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		t.Fatal("the decision service received no request")
	}
	return s.requests[len(s.requests)-1]
}

// answerWith writes a choice answer carrying both of Laya's confidence fields.
func answerWith(w http.ResponseWriter, choice string, answerConfidence, confidence float64) {
	_, _ = io.WriteString(w, `{"model":"laya-multilingual","answers":{"theme":{"type":"choice",`+
		`"choice":"`+choice+`","probabilities":{"`+choice+`":0.83},`+
		`"confidence":`+ftoa(confidence)+`,"answer_confidence":`+ftoa(answerConfidence)+`}}}`)
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

// defaultOptions mirrors what classify offers for the default theme set.
func defaultOptions() []classify.ThemeOption {
	return []classify.ThemeOption{
		{Slug: "mountain", Description: "mountains, peaks, alpine landscapes, hiking, snow, skiing"},
		{Slug: "special-events", Description: "weddings, parties, concerts, ceremonies, celebrations"},
		{Slug: "cook", Description: "food, meals, cooking, plated dishes, restaurants"},
		{Slug: "family", Description: "people, portraits, family gatherings, children, daily life"},
		{Slug: "other", Description: "the photos fit none of the other themes"},
	}
}

func decision(descriptions ...string) classify.Decision {
	return classify.Decision{Descriptions: descriptions, Options: defaultOptions(), Fallback: "other"}
}

func newClient(t *testing.T, cfg decide.Config) *decide.Client {
	t.Helper()
	c, err := decide.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestClientSendsTheKeyOnlyWhenSet(t *testing.T) {
	for _, tc := range []struct {
		name, key, want string
	}{
		{"no key", "", ""},
		{"key", "k3y-abc", "Bearer k3y-abc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A key in the environment must not leak in through gutcheck's own lookup:
			// the transport already read it, and an empty Config key means none.
			t.Setenv("LAYA_API_KEY", "from-the-environment")
			svc, srv := newService(t, nil)
			c := newClient(t, decide.Config{Backend: decide.Laya, URL: srv.URL, Model: "multilingual", APIKey: tc.key})
			if _, err := c.Decide(context.Background(), decision("A plate of pasta.")); err != nil {
				t.Fatalf("Decide: %v", err)
			}
			if got := svc.last(t).auth; got != tc.want {
				t.Errorf("Authorization = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestClientUsesItsOwnHTTPClient(t *testing.T) {
	c := newClient(t, decide.Config{Backend: decide.Laya, URL: "http://127.0.0.1:9", Model: "multilingual"})
	hc := c.HTTPClient()
	if hc == nil || hc == http.DefaultClient {
		t.Fatal("the client must not share http.DefaultClient")
	}
	tr, ok := hc.Transport.(*http.Transport)
	if !ok || tr.MaxIdleConnsPerHost <= http.DefaultMaxIdleConnsPerHost {
		t.Errorf("transport = %#v; want a cloned transport with a larger keep-alive pool", hc.Transport)
	}
}

func TestReadySendsTheFixedStateAndTheRealOptions(t *testing.T) {
	svc, srv := newService(t, nil)
	c := newClient(t, decide.Config{Backend: decide.Laya, URL: srv.URL, Model: "multilingual"})
	d := decision("unused")
	d.Context = []string{"taken: 2025-03-02 09:10 (local time)"}
	if err := c.Ready(context.Background(), d); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	req := decodeRequest(t, svc.last(t).body)
	if req.State != "Photo 1: A test photo." {
		t.Errorf("readiness state = %q; want the fixed test photo, no context", req.State)
	}
	if len(req.Questions.Theme.Criteria) != len(defaultOptions()) {
		t.Errorf("readiness offers %d options; want the run's %d", len(req.Questions.Theme.Criteria), len(defaultOptions()))
	}
}
