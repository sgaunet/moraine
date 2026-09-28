package decide_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sgaunet/gutcheck"

	"github.com/sgaunet/moraine/internal/classify"
	"github.com/sgaunet/moraine/internal/decide"
)

// fastClient shortens the retry backoff so the retried rows of the error table do not
// each cost a second and a half.
func fastClient(t *testing.T, url, key string, attempt time.Duration) *decide.Client {
	t.Helper()
	c, err := decide.NewWithTimeouts(decide.Config{Backend: decide.Laya, URL: url, Model: "multilingual", APIKey: key},
		attempt, attempt, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func status(code int, body string) func(int, http.ResponseWriter) {
	return func(_ int, w http.ResponseWriter) {
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}
}

// TestErrorTable covers contracts/decision-request.md "Errors → behaviour" from the
// client's side: each failure comes back as an error that still is the gutcheck error
// (so a caller can tell them apart), after exactly the attempts the retry policy
// allows.
func TestErrorTable(t *testing.T) {
	tests := []struct {
		name     string
		answer   func(int, http.ResponseWriter)
		key      string
		requests int    // attempts the service must see
		is       error  // sentinel the error must match, if any
		as       any    // gutcheck error type the error must unwrap to, if any
		mention  string // text the error must contain
	}{
		{"401 without a key", status(http.StatusUnauthorized, `{"detail":"invalid or missing bearer token"}`), "",
			1, gutcheck.ErrAuthentication, nil, "LAYA_API_KEY"},
		{"401 with a key", status(http.StatusUnauthorized, `{"detail":"invalid or missing bearer token"}`), "k3y-wrong",
			1, gutcheck.ErrAuthentication, nil, "LAYA_API_KEY"},
		{"403", status(http.StatusForbidden, `{}`), "k3y", 1, gutcheck.ErrPermissionDenied, nil, "LAYA_API_KEY"},
		{"404 bad model", status(http.StatusNotFound, `{"detail":"unknown model"}`), "", 1, gutcheck.ErrNotFound, nil, "404"},
		{"413 too many options", status(http.StatusRequestEntityTooLarge, `{"detail":"too many options"}`), "",
			1, nil, new(*gutcheck.APIError), "413"},
		{"422", status(http.StatusUnprocessableEntity, `{}`), "", 1, gutcheck.ErrUnprocessableEntity, nil, "422"},
		{"500 is retried, then fails", status(http.StatusInternalServerError, `oops`), "", 3, gutcheck.ErrServer, nil, "500"},
		{"malformed JSON", status(http.StatusOK, `{not json`), "", 1, nil, new(*gutcheck.ResponseValidationError), "(body)"},
		{"missing theme answer", status(http.StatusOK, `{"answers":{}}`), "",
			1, nil, new(*gutcheck.ResponseValidationError), "answers.theme"},
	}
	calls := map[string]func(*decide.Client) error{
		"Ready": func(c *decide.Client) error { return c.Ready(context.Background(), decision("x")) },
		"Decide": func(c *decide.Client) error {
			_, err := c.Decide(context.Background(), decision("A plate of pasta."))
			return err
		},
	}
	for _, tc := range tests {
		for call, run := range calls {
			t.Run(tc.name+"/"+call, func(t *testing.T) {
				svc, srv := newService(t, tc.answer)
				err := run(fastClient(t, srv.URL, tc.key, 5*time.Second))
				if err == nil {
					t.Fatal("want an error")
				}
				if n := svc.count(); n != tc.requests {
					t.Errorf("service saw %d requests; want %d", n, tc.requests)
				}
				if tc.is != nil && !errors.Is(err, tc.is) {
					t.Errorf("errors.Is(%v, %v) = false", err, tc.is)
				}
				if tc.as != nil && !errors.As(err, tc.as) {
					t.Errorf("errors.As(%v, %T) = false", err, tc.as)
				}
				if !strings.Contains(err.Error(), tc.mention) {
					t.Errorf("error %q does not mention %q", err, tc.mention)
				}
				if tc.key != "" && strings.Contains(err.Error(), tc.key) {
					t.Errorf("error leaks the key: %v", err)
				}
			})
		}
	}
}

func TestConnectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	err := fastClient(t, url, "", time.Second).Ready(context.Background(), decision("x"))
	var connErr *gutcheck.ConnectionError
	if !errors.As(err, &connErr) {
		t.Fatalf("err = %v; want a *gutcheck.ConnectionError", err)
	}
}

// A 503 busy answer is honoured and retried; the next attempt's answer stands.
func TestBusyIsRetried(t *testing.T) {
	svc, srv := newService(t, func(n int, w http.ResponseWriter) {
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		answerWith(w, "cook", 0.9, 0.5)
	})
	v, err := fastClient(t, srv.URL, "", 5*time.Second).Decide(context.Background(), decision("A plate of pasta."))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if v != (classify.Verdict{Theme: "cook", Confidence: 0.9, Decided: true}) || svc.count() != 2 {
		t.Errorf("verdict = %+v after %d requests; want cook after 2", v, svc.count())
	}
}

// A timed-out attempt is not retried: the inference it started is still running on
// the server, and a second request would only queue behind it.
func TestTimeoutIsNotRetried(t *testing.T) {
	release := make(chan struct{})
	svc, srv := newService(t, func(_ int, w http.ResponseWriter) {
		<-release
		answerWith(w, "cook", 0.9, 0.5)
	})
	defer close(release)
	_, err := fastClient(t, srv.URL, "", 50*time.Millisecond).Decide(context.Background(), decision("x"))
	if !errors.Is(err, gutcheck.ErrTimeout) {
		t.Fatalf("err = %v; want a timeout", err)
	}
	if n := svc.count(); n != 1 {
		t.Errorf("service saw %d requests; want exactly 1", n)
	}
}

// An interrupt is returned as the context's own error, so the caller's interrupt
// path — not its failure path — reports it.
func TestCancellationIsTheContextError(t *testing.T) {
	release := make(chan struct{})
	_, srv := newService(t, func(_ int, _ http.ResponseWriter) { <-release })
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	err := fastClient(t, srv.URL, "", 5*time.Second).Ready(ctx, decision("x"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v; want context.Canceled", err)
	}
}
