package app_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/app"
	"github.com/sgaunet/moraine/internal/config"
	"github.com/sgaunet/moraine/internal/organize"
)

// pacedOllamaStub serves /api/tags and /api/chat, handing each classification
// request to onChat with its 1-based arrival ordinal and returning the theme onChat
// chooses. onChat may block, which is how a test pins the ordering between
// classifying one event and copying another. Requests arrive in cluster order
// because the look-ahead has exactly one producer.
func pacedOllamaStub(t *testing.T, onChat func(nth int) string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	var seen int
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(`{"models":[{"name":"` + stubModel + `"}]}`))
			return
		}
		var body struct {
			Messages []struct {
				Images []string `json:"images"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if len(body.Messages) == 0 {
			_, _ = w.Write([]byte(`{"message":{"content":""}}`)) // warm-up
			return
		}
		mu.Lock()
		seen++
		nth := seen
		mu.Unlock()
		_, _ = w.Write([]byte(`{"message":{"content":"` + onChat(nth) + `"}}`))
	}))
}

// eventSource builds n single-photo events, each a day further from the last so the
// gap always splits them.
func eventSource(t *testing.T, n int) string {
	t.Helper()
	src := t.TempDir()
	for i := range n {
		p := filepath.Join(src, string(rune('a'+i))+".png")
		makePNG(t, p)
		when := modTime.Add(time.Duration(i) * 48 * time.Hour)
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}
	}
	return src
}

func modelCfg(src, dest, url string) config.Config {
	cfg := baseCfg(src, dest, true)
	cfg.Sample = 1
	cfg.Model = stubModel
	cfg.OllamaURL = url
	return cfg
}

// TestOrganizeClassifiesAheadOfPlacement is the regression test for issue #9's
// classification item: the run used to classify event N, copy event N, then
// classify event N+1, so the model round-trip and the copy I/O never overlapped.
// The first placement blocks until the second event has been classified, which can
// only happen if the two stages run at the same time.
func TestOrganizeClassifiesAheadOfPlacement(t *testing.T) {
	src, dest := eventSource(t, 3), t.TempDir()
	secondClassified := make(chan struct{})
	var once sync.Once
	srv := pacedOllamaStub(t, func(nth int) string {
		if nth == 2 {
			once.Do(func() { close(secondClassified) })
		}
		return "mountain"
	})
	defer srv.Close()

	var firstResult sync.Once
	waited := make(chan bool, 1)
	onResult := func(organize.Result) {
		firstResult.Do(func() {
			select {
			case <-secondClassified:
				waited <- true
			case <-time.After(3 * time.Second):
				waited <- false // serial code lands here: nothing is classified ahead
			}
		})
	}

	if _, err := app.Organize(context.Background(), modelCfg(src, dest, srv.URL), quietLogger(), onResult, nil); err != nil {
		t.Fatalf("Organize: %v", err)
	}
	if !<-waited {
		t.Error("the second event was not classified while the first was being placed")
	}
}

// TestOrganizeLookAheadKeepsEventsInClusterOrder is the stdout-contract guard: the
// events array and the per-file record stream are ordered by capture time, and
// classifying ahead must not disturb that. The latency is inverted on purpose — the
// earliest event is the slowest to classify — so a pipeline that let results
// overtake each other would be caught.
func TestOrganizeLookAheadKeepsEventsInClusterOrder(t *testing.T) {
	src, dest := eventSource(t, 3), t.TempDir()
	themesByArrival := []string{"mountain", "family", "nature"}
	srv := pacedOllamaStub(t, func(nth int) string {
		if nth == 1 {
			time.Sleep(150 * time.Millisecond) // the earliest event answers last
		}
		return themesByArrival[nth-1]
	})
	defer srv.Close()

	var streamed []string
	onResult := func(r organize.Result) { streamed = append(streamed, r.Theme) }

	sum, err := app.Organize(context.Background(), modelCfg(src, dest, srv.URL), quietLogger(), onResult, nil)
	if err != nil {
		t.Fatalf("Organize: %v", err)
	}
	if len(sum.Events) != 3 {
		t.Fatalf("events = %d; want 3", len(sum.Events))
	}
	for i, want := range themesByArrival {
		if sum.Events[i].Theme != want {
			t.Errorf("event %d theme = %q; want %q", i, sum.Events[i].Theme, want)
		}
	}
	for i := 1; i < len(sum.Events); i++ {
		if !sum.Events[i-1].Start.Before(sum.Events[i].Start) {
			t.Errorf("events are not in capture-time order: %v then %v",
				sum.Events[i-1].Start, sum.Events[i].Start)
		}
	}
	if !reflect.DeepEqual(streamed, themesByArrival) {
		t.Errorf("record stream = %v; want %v — records must arrive event by event, in order",
			streamed, themesByArrival)
	}
}

// TestOrganizeLookAheadKeepsTheSummaryDeterministic complements TestOrganizeJobs:
// the same input must produce the same Summary however the classification latency
// falls out.
func TestOrganizeLookAheadKeepsTheSummaryDeterministic(t *testing.T) {
	run := func(t *testing.T, delay time.Duration) app.Summary {
		t.Helper()
		src, dest := eventSource(t, 4), t.TempDir()
		srv := pacedOllamaStub(t, func(nth int) string {
			// Jitter: a different event is the slow one on each run.
			if time.Duration(nth)*delay > 0 {
				time.Sleep(time.Duration(nth%3) * delay)
			}
			return "mountain"
		})
		defer srv.Close()
		sum, err := app.Organize(context.Background(), modelCfg(src, dest, srv.URL), quietLogger(), nil, nil)
		if err != nil {
			t.Fatalf("Organize: %v", err)
		}
		return sum
	}

	fast, slow := run(t, 0), run(t, 40*time.Millisecond)
	if !reflect.DeepEqual(fast, slow) {
		t.Errorf("classification latency changed the outcome:\n fast %+v\n slow %+v", fast, slow)
	}
}

// TestOrganizeCancellationStopsTheLookAhead pins Principle VI: an interrupt must
// stop the classifier that is running ahead, not merely stop consuming it, and
// Organize must not outlive it or leak it.
func TestOrganizeCancellationStopsTheLookAhead(t *testing.T) {
	src, dest := eventSource(t, 6), t.TempDir()
	var mu sync.Mutex
	var chats int
	srv := pacedOllamaStub(t, func(int) string {
		mu.Lock()
		chats++
		mu.Unlock()
		return "mountain"
	})
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var once sync.Once
	onResult := func(organize.Result) { once.Do(cancel) }

	start := time.Now()
	sum, err := app.Organize(ctx, modelCfg(src, dest, srv.URL), quietLogger(), onResult, nil)
	if err == nil {
		t.Fatal("expected the run to report its cancellation")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Organize took %v to unwind; the look-ahead was waited out, not cancelled", elapsed)
	}
	// Photos the run never reached are not failures.
	if sum.Errors != 0 {
		t.Errorf("Errors = %d; a cancelled run must not report placement failures", sum.Errors)
	}

	mu.Lock()
	settled := chats
	mu.Unlock()
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if chats != settled {
		t.Errorf("classification requests grew from %d to %d after Organize returned: "+
			"the look-ahead outlived the run", settled, chats)
	}
}

// TestOrganizeInterruptBeforeAnyClusterClassifies pins that the look-ahead checks
// for cancellation before its first call, not after: an already-interrupted run
// must not pull a vision model into memory to answer a question nobody will read.
func TestOrganizeInterruptBeforeAnyClusterClassifies(t *testing.T) {
	src, dest := eventSource(t, 4), t.TempDir()
	var mu sync.Mutex
	var chats int
	srv := pacedOllamaStub(t, func(int) string {
		mu.Lock()
		chats++
		mu.Unlock()
		return "mountain"
	})
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	sum, err := app.Organize(ctx, modelCfg(src, dest, srv.URL), quietLogger(), nil, nil)
	if err == nil {
		t.Fatal("expected the run to report its cancellation")
	}
	if sum.Groups != 0 {
		t.Errorf("Groups = %d; want 0 (nothing was placed)", sum.Groups)
	}
	mu.Lock()
	defer mu.Unlock()
	if chats != 0 {
		t.Errorf("classification requests = %d; want 0 on an already-cancelled run", chats)
	}
}
