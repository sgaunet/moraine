package classify_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/classify"
	"github.com/sgaunet/moraine/internal/photo"
)

func TestTallyVotes(t *testing.T) {
	v := func(theme string) classify.Verdict { return classify.Verdict{Theme: theme} }
	tests := []struct {
		name           string
		verdicts       []classify.Verdict
		wantTheme      string
		wantConfidence float64
	}{
		{"unanimous", []classify.Verdict{v("mountain"), v("mountain"), v("mountain")}, "mountain", 1},
		{"majority", []classify.Verdict{v("mountain"), v("mountain"), v("family")}, "mountain", 2.0 / 3.0},
		// A bare plurality wins but says so: 2 of 5 is reported as 0.4, which
		// --min-confidence can reject.
		{"plurality", []classify.Verdict{v("nature"), v("nature"), v("family"), v("mountain"), v("special-events")}, "nature", 0.4},
		// A tie invents no winner: alphabetical accident is not a decision.
		{"tie abstains", []classify.Verdict{v("family"), v("mountain")}, "", 0},
		{"three-way tie abstains", []classify.Verdict{v("family"), v("mountain"), v("nature")}, "", 0},
		// An abstention is an opinion, so a sample that mostly fits nothing abstains.
		{"abstentions win", []classify.Verdict{v(""), v(""), v("family")}, "", 0},
		{"abstentions lose", []classify.Verdict{v(""), v("family"), v("family")}, "family", 2.0 / 3.0},
		{"empty", nil, "", 0},
		{"single", []classify.Verdict{v("family")}, "family", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classify.TallyVotes(tc.verdicts)
			if got.Theme != tc.wantTheme {
				t.Errorf("theme = %q; want %q", got.Theme, tc.wantTheme)
			}
			if diff := got.Confidence - tc.wantConfidence; diff > 1e-9 || diff < -1e-9 {
				t.Errorf("confidence = %g; want %g", got.Confidence, tc.wantConfidence)
			}
		})
	}
}

// TestTallyVotesIsDeterministic pins that the winner does not depend on the order
// the votes were cast in — map iteration order would otherwise leak into a tie.
func TestTallyVotesIsDeterministic(t *testing.T) {
	v := func(theme string) classify.Verdict { return classify.Verdict{Theme: theme} }
	forward := []classify.Verdict{v("family"), v("mountain"), v("mountain")}
	reverse := []classify.Verdict{v("mountain"), v("mountain"), v("family")}
	for range 20 {
		a, b := classify.TallyVotes(forward), classify.TallyVotes(reverse)
		if a != b || a.Theme != "mountain" {
			t.Fatalf("tally = %+v and %+v; want both mountain", a, b)
		}
	}
}

// votingCluster builds a cluster of n JPEGs, large enough that voting applies.
func votingCluster(t *testing.T, n int) photo.Cluster {
	t.Helper()
	dir := t.TempDir()
	ps := make([]photo.Photo, 0, n)
	for i := range n {
		p := filepath.Join(dir, string(rune('a'+i))+".jpg")
		writeTinyJPEG(t, p)
		ps = append(ps, photo.Photo{Path: p, Name: filepath.Base(p), Format: photo.JPEG})
	}
	return photo.Cluster{Photos: ps}
}

// answerSequence serves one canned category per classification request, in arrival
// order, so a test can stage a disagreeing vote. The warm-up call is answered
// separately and consumes no answer.
//
// It used to promise answers[i] to the i-th photo. Now that a vote's calls overlap,
// the pairing of an answer to a photo is no longer fixed, so a caller may depend
// only on the multiset of answers — which is all a tally reads anyway.
func answerSequence(t *testing.T, answers ...string) (http.HandlerFunc, func() int) {
	t.Helper()
	var (
		mu    sync.Mutex
		calls int
	)
	h := chatOnly(t, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		i := calls
		calls++
		mu.Unlock()
		if i >= len(answers) {
			t.Errorf("classification request %d has no staged answer", i+1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"message":{"content":"{\"category\":\"` + answers[i] + `\"}"}}`))
	})
	return h, func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
}

// TestVoteOnePerSampledPhoto pins the shape of the vote: one model call per sampled
// photo, each carrying exactly one image, and the majority wins with the vote share
// as its confidence.
func TestVoteOnePerSampledPhoto(t *testing.T) {
	var images []int
	var mu sync.Mutex
	srv := httptest.NewServer(chatOnly(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var req struct {
			Messages []struct {
				Images []string `json:"images"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Error(err)
			return
		}
		n := 0
		for _, m := range req.Messages {
			n += len(m.Images)
		}
		mu.Lock()
		images = append(images, n)
		i := len(images)
		mu.Unlock()
		// Two mountains and one family: mountain wins 2 of 3. Which photo draws the
		// dissenting answer is a race now that the votes run concurrently, and
		// deliberately so — the tally must not depend on it.
		answer := "mountain"
		if i == 2 {
			answer = "family"
		}
		_, _ = w.Write([]byte(`{"message":{"content":"{\"category\":\"` + answer + `\"}"}}`))
	}))
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 3, themes)
	oc.Vote = true
	got, err := oc.Classify(context.Background(), votingCluster(t, 6))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got.Theme != "mountain" {
		t.Errorf("theme = %q; want mountain (2 of 3 votes)", got.Theme)
	}
	if want := 2.0 / 3.0; got.Confidence < want-1e-9 || got.Confidence > want+1e-9 {
		t.Errorf("confidence = %g; want %g (the vote share)", got.Confidence, want)
	}
	if len(images) != 3 {
		t.Fatalf("model calls = %d; want 3 (one per sampled photo)", len(images))
	}
	for i, n := range images {
		if n != 1 {
			t.Errorf("call %d carried %d images; want exactly 1", i+1, n)
		}
	}
}

// TestVoteSkippedForSmallGroup pins that a group the model already sees in full is
// classified in a single call: voting there would pay N times for the same photos.
func TestVoteSkippedForSmallGroup(t *testing.T) {
	h, calls := answerSequence(t, "mountain")
	srv := httptest.NewServer(h)
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 3, themes)
	oc.Vote = true
	got, err := oc.Classify(context.Background(), votingCluster(t, classify.SmallGroupMax))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got.Theme != "mountain" {
		t.Errorf("theme = %q; want mountain", got.Theme)
	}
	if n := calls(); n != 1 {
		t.Errorf("model calls = %d; want 1 (no vote for a small group)", n)
	}
}

// TestVoteTieAbstains pins that a split vote produces no theme, so Label falls
// through to the heuristic and then the fallback rather than picking a side.
func TestVoteTieAbstains(t *testing.T) {
	h, _ := answerSequence(t, "mountain", "family")
	srv := httptest.NewServer(h)
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 2, themes)
	oc.Vote = true
	got, err := oc.Classify(context.Background(), votingCluster(t, 4))
	if err != nil {
		t.Fatalf("a tie is an abstention, not a failure: %v", err)
	}
	if got.Theme != "" || got.Confidence != 0 {
		t.Errorf("verdict = %+v; want an abstention", got)
	}
}

// TestVoteSurvivesOneFailedCall pins that a photo whose call fails simply does not
// vote: the remaining photos still decide the event.
func TestVoteSurvivesOneFailedCall(t *testing.T) {
	var mu sync.Mutex
	var n int
	srv := httptest.NewServer(chatOnly(t, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		n++
		i := n
		mu.Unlock()
		// Exactly one request fails — whichever arrives first, now that the votes
		// overlap. Which photo is lost must not change the event's verdict.
		if i == 1 {
			w.WriteHeader(http.StatusBadRequest) // deterministic failure: not retried
			return
		}
		_, _ = w.Write([]byte(`{"message":{"content":"{\"category\":\"family\"}"}}`))
	}))
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 3, themes)
	oc.Vote = true
	got, err := oc.Classify(context.Background(), votingCluster(t, 6))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got.Theme != "family" || got.Confidence != 1 {
		t.Errorf("verdict = %+v; want family at confidence 1 (2 of 2 surviving votes)", got)
	}
}

// TestVoteAllCallsFailIsError pins the difference between "undecided" and
// "unavailable": if no photo could be classified at all, the caller must see an
// error, not a quiet abstention.
func TestVoteAllCallsFailIsError(t *testing.T) {
	srv := httptest.NewServer(chatOnly(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 3, themes)
	oc.Vote = true
	if _, err := oc.Classify(context.Background(), votingCluster(t, 6)); err == nil {
		t.Fatal("expected an error when every vote fails")
	}
}

// barrierHandler holds every classification request until want of them are in
// flight at once, then releases them all. A serial caller can never reach the
// barrier, so a test using it fails on the escape timeout instead of hanging.
func barrierHandler(t *testing.T, want int, answer string) (http.HandlerFunc, func() int) {
	t.Helper()
	var (
		mu       sync.Mutex
		inFlight int
		peak     int
	)
	reached := make(chan struct{})
	var once sync.Once
	h := chatOnly(t, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		atBarrier := inFlight >= want
		mu.Unlock()
		if atBarrier {
			once.Do(func() { close(reached) })
		}
		select {
		case <-reached:
		case <-time.After(2 * time.Second): // a serial caller lands here
		}
		mu.Lock()
		inFlight--
		mu.Unlock()
		_, _ = w.Write([]byte(`{"message":{"content":"{\"category\":\"` + answer + `\"}"}}`))
	})
	return h, func() int {
		mu.Lock()
		defer mu.Unlock()
		return peak
	}
}

// TestVoteRunsItsCallsConcurrently is the regression test for issue #9's voting
// item: each sampled photo used to be a blocking call of its own, so N photos cost
// N sequential round-trips. The barrier can only clear if the calls overlap.
func TestVoteRunsItsCallsConcurrently(t *testing.T) {
	h, peak := barrierHandler(t, 2, "mountain")
	srv := httptest.NewServer(h)
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 3, themes)
	oc.Vote = true
	got, err := oc.Classify(context.Background(), votingCluster(t, 6))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got.Theme != "mountain" {
		t.Errorf("theme = %q; want mountain", got.Theme)
	}
	if n := peak(); n < 2 {
		t.Errorf("peak in-flight requests = %d; want at least 2 — the votes did not overlap", n)
	}
}

// TestVoteWorkersBoundsTheCallsInFlight pins that the fan-out is bounded, and that
// a caller can still pin the serial behaviour. Ollama serialises per model, so an
// unbounded fan-out would only queue requests server-side while each one spends its
// own timeout budget waiting its turn.
func TestVoteWorkersBoundsTheCallsInFlight(t *testing.T) {
	var (
		mu       sync.Mutex
		inFlight int
		peak     int
	)
	srv := httptest.NewServer(chatOnly(t, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		mu.Unlock()
		time.Sleep(20 * time.Millisecond) // long enough for an unbounded fan-out to pile up
		mu.Lock()
		inFlight--
		mu.Unlock()
		_, _ = w.Write([]byte(`{"message":{"content":"{\"category\":\"mountain\"}"}}`))
	}))
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 4, themes)
	oc.Vote, oc.VoteWorkers = true, 1
	if _, err := oc.Classify(context.Background(), votingCluster(t, 8)); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if peak != 1 {
		t.Errorf("peak in-flight requests = %d with VoteWorkers=1; want 1", peak)
	}
}

// TestVoteVerdictIsIdenticalSerialAndConcurrent is the equivalence proof: which
// photo draws which staged answer is now a race, and the verdict must not depend on
// it. tallyVotes is order-independent by construction; this pins that the fan-out
// did not smuggle an ordering dependency in around it.
func TestVoteVerdictIsIdenticalSerialAndConcurrent(t *testing.T) {
	run := func(t *testing.T, workers int) classify.Verdict {
		t.Helper()
		h, _ := answerSequence(t, "mountain", "family", "mountain")
		srv := httptest.NewServer(h)
		defer srv.Close()

		oc := classify.NewOllama(srv.URL, "m", 3, themes)
		oc.Vote, oc.VoteWorkers = true, workers
		got, err := oc.Classify(context.Background(), votingCluster(t, 6))
		if err != nil {
			t.Fatalf("workers=%d: %v", workers, err)
		}
		return got
	}

	serial := run(t, 1)
	if serial.Theme != "mountain" {
		t.Fatalf("serial theme = %q; want mountain (2 of 3)", serial.Theme)
	}
	// Repeat: the answer-to-photo pairing reshuffles on every concurrent run.
	for range 5 {
		if concurrent := run(t, 4); concurrent != serial {
			t.Fatalf("verdict = %+v concurrently, %+v serially", concurrent, serial)
		}
	}
}

// TestVoteAllFailedReportsAStableError pins that the error a wholly failed vote
// reports is a fact about the run rather than about which goroutine finished last.
// The loop used to keep the last error it saw; under a fan-out that is whichever
// call happened to return last, so the first vote's error is reported instead.
func TestVoteAllFailedReportsAStableError(t *testing.T) {
	srv := httptest.NewServer(chatOnly(t, func(w http.ResponseWriter, r *http.Request) {
		// Each photo fails with a message naming itself, so the reported error
		// identifies which vote it came from.
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("refused-" + strconv.Itoa(len(body))))
	}))
	defer srv.Close()

	oc := classify.NewOllama(srv.URL, "m", 3, themes)
	oc.Vote = true
	seen := make(map[string]bool)
	for range 5 {
		_, err := oc.Classify(context.Background(), votingCluster(t, 6))
		if err == nil {
			t.Fatal("expected an error when every vote fails")
		}
		seen[err.Error()] = true
	}
	if len(seen) != 1 {
		t.Errorf("a wholly failed vote reported %d different errors across identical runs; want 1", len(seen))
	}
}
