package classify_test

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/classify"
)

// quietLogger keeps a test's own output clean: these tests provoke warnings on
// purpose and none of them assert on a log line.
func quietLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// connCounter counts the connections a server accepts, so a test can prove
// keep-alive is working rather than assume it. The callback runs on the server's
// own goroutines, hence the mutex.
type connCounter struct {
	mu   sync.Mutex
	seen int
}

func (c *connCounter) track(srv *httptest.Server) {
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state != http.StateNew {
			return
		}
		c.mu.Lock()
		c.seen++
		c.mu.Unlock()
	}
}

func (c *connCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen
}

// TestNewOllamaSizesTheConnectionPool pins the transport decision itself: a bare
// &http.Client{} silently uses http.DefaultTransport, whose MaxIdleConnsPerHost is
// 2 — invisible while calls are serial, a per-call TCP handshake as soon as they
// are not.
func TestNewOllamaSizesTheConnectionPool(t *testing.T) {
	oc := classify.NewOllama("http://127.0.0.1:11434", "m", 3, []string{"mountain"})
	tr, ok := oc.HTTP.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("HTTP.Transport = %T; want an explicit *http.Transport", oc.HTTP.Transport)
	}
	if tr.MaxIdleConnsPerHost < 4 {
		t.Errorf("MaxIdleConnsPerHost = %d; want room for the vote fan-out", tr.MaxIdleConnsPerHost)
	}
	// Cloned from DefaultTransport, not built fresh: a user behind an HTTP proxy
	// must not lose it to this optimisation.
	if tr.Proxy == nil {
		t.Error("Proxy is nil: the transport was built fresh instead of cloned from DefaultTransport")
	}
	// A transport-wide header timeout would abort a legitimate cold model load;
	// the per-call context deadline is the bound that belongs here.
	if tr.ResponseHeaderTimeout != 0 {
		t.Errorf("ResponseHeaderTimeout = %v; want none", tr.ResponseHeaderTimeout)
	}
}

// TestClassifyDrainsAnOversizeResponseAndKeepsTheConnection is the regression test
// for the undrained body: net/http only pools a connection once its body reaches
// EOF, and the response is read through a LimitReader, so a reply larger than
// maxResponseBytes used to cost a fresh TCP handshake on every call.
func TestClassifyDrainsAnOversizeResponseAndKeepsTheConnection(t *testing.T) {
	const oversize = 5 << 20 // larger than maxResponseBytes (4 MiB)
	conns := &connCounter{}
	srv := httptest.NewUnstartedServer(chatOnly(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"message":{"content":"{\"category\":\"mountain\"}"},"pad":%q}`,
			strings.Repeat("x", oversize))
	}))
	conns.track(srv)
	srv.Start()
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 1, []string{"mountain"})
	oc.Logger = quietLogger()
	for range 3 {
		// The answer is lost either way — the LimitReader truncates it and the JSON
		// no longer parses. That is a separate wart, asserted here so it is recorded
		// rather than assumed; what this test is about is the connection.
		if _, err := oc.Classify(context.Background(), jpegCluster(t)); err == nil {
			t.Fatal("a truncated response must not parse as a verdict")
		}
	}
	if got := conns.count(); got != 1 {
		t.Errorf("server accepted %d connections for 3 calls; want 1 (the body is not being drained)", got)
	}
}

// TestPreflightDrainsAnOversizeTagsResponse covers the same defect on the other
// call site.
func TestPreflightDrainsAnOversizeTagsResponse(t *testing.T) {
	const oversize = 5 << 20
	conns := &connCounter{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"models":[{"name":"m"}],"pad":%q}`, strings.Repeat("x", oversize))
	}))
	conns.track(srv)
	srv.Start()
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 1, []string{"mountain"})
	oc.Logger = quietLogger()
	for range 3 {
		_ = oc.Preflight(context.Background())
	}
	if got := conns.count(); got != 1 {
		t.Errorf("server accepted %d connections for 3 preflights; want 1", got)
	}
}

// TestClassifyDoesNotHangOnAnEndlessBody proves the drain is bounded in both ways
// that matter: by a byte cap, and by the call's own deadline. An unbounded drain of
// a runaway stream would be a denial of service of its own making.
func TestClassifyDoesNotHangOnAnEndlessBody(t *testing.T) {
	srv := httptest.NewServer(chatOnly(t, func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			return
		}
		chunk := strings.Repeat("x", 32<<10)
		for {
			select {
			case <-r.Context().Done():
				return
			default:
			}
			if _, err := w.Write([]byte(chunk)); err != nil {
				return
			}
			flusher.Flush()
		}
	}))
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 1, []string{"mountain"})
	oc.Logger = quietLogger()
	oc.Timeout = 500 * time.Millisecond

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = oc.Classify(context.Background(), jpegCluster(t))
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Classify hung: the drain is not bounded")
	}
}
