package classify

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/sgaunet/moraine/internal/photo"
)

// Voting classifies each sampled photo on its own instead of showing the model the
// whole sample at once, then lets the answers vote. It exists because a single call
// over several images gives the model one chance to be right about a mixed event and
// no way to say how mixed it was: a lunch stop inside a hiking day, a party that
// starts at a dinner table. Per-photo answers disagree in that case, and the share
// of votes the winner takes is a confidence signal a single call cannot produce.
//
// The cost is one model call per sampled photo rather than one per group, which is
// why it is opt-in (--vote) and applies only to groups larger than SmallGroupMax —
// a group the model already sees in full has nothing to gain from it.

// vote is one photo's answer, kept in a slot of its own so the reduction below
// reads the same whatever order the calls came back in.
type vote struct {
	verdict Verdict
	err     error
}

// voteWorkers is the fan-out width for one group, never wider than there are votes
// to cast.
func (o *OllamaClassifier) voteWorkers(votes int) int {
	n := o.VoteWorkers
	if n < 1 {
		n = defaultVoteWorkers
	}
	return min(n, votes)
}

// classifyByVote asks about each image separately and reduces the answers with
// tallyVotes. A photo whose call fails simply does not vote; only when every one of
// them fails is the whole classification an error, since that means the model was
// unavailable rather than undecided.
//
// The calls run concurrently, bounded by voteWorkers, following the same shape as
// the EXIF stage: a buffered channel as the semaphore, taken before the goroutine
// starts so at most that many exist at once, and a WaitGroup to join them. Each vote
// keeps its own retry budget, so the retry load is bounded by the fan-out width
// rather than by the number of photos.
//
// Every goroutine writes one slot of its own and reads none, so the results need no
// lock and still come back in photo order — which is what keeps the debug lines and
// the error below reproducible rather than dependent on scheduling.
func (o *OllamaClassifier) classifyByVote(ctx context.Context, c photo.Cluster, images []string) (Verdict, error) {
	votes := make([]vote, len(images))
	sem := make(chan struct{}, o.voteWorkers(len(images)))
	var wg sync.WaitGroup
	for i, img := range images {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			// Each vote is a model call of its own, so it gets its own Timeout budget
			// rather than a share of one: a slow photo must not spend another's time.
			voteCtx, cancel := o.bounded(ctx)
			defer cancel()
			v, err := o.ask(voteCtx, c, []string{img})
			votes[i] = vote{verdict: v, err: err}
		}()
	}
	wg.Wait()

	verdicts := make([]Verdict, 0, len(images))
	var firstErr error
	for i, v := range votes {
		if v.err != nil {
			// The first vote to fail by photo order, not the last to fail by clock:
			// under a fan-out the latter is whichever goroutine happened to finish
			// last, which is not a fact about the run.
			if firstErr == nil {
				firstErr = v.err
			}
			o.log().Debug("vote failed", "vote", i+1, "of", len(images), "err", v.err)
			continue
		}
		verdicts = append(verdicts, v.verdict)
	}
	if len(verdicts) == 0 {
		return Verdict{}, fmt.Errorf("every vote failed: %w", firstErr)
	}
	won := tallyVotes(verdicts)
	o.log().Debug("vote result",
		"theme", won.Theme, "confidence", won.Confidence, "votes", len(verdicts), "of", len(images))
	return won, nil
}

// tallyVotes reduces per-photo verdicts to one. Each photo casts a vote for its
// theme, or for abstention when nothing fits it — an abstention is an opinion here,
// not a missing vote, so a sample that mostly fits no theme abstains as a whole. The
// theme with the most votes wins, and the share of the votes it took becomes the
// confidence, so a bare plurality (2 of 5) is reported as 0.4 rather than dressed up
// as a decision.
//
// An exact tie is no verdict at all: breaking it on slug order would invent a winner
// out of alphabetical accident. Callers read a zero-Theme Verdict as an abstention
// and fall through to the heuristic, which is the honest answer for a genuinely
// split event.
func tallyVotes(verdicts []Verdict) Verdict {
	if len(verdicts) == 0 {
		return Verdict{}
	}
	counts := make(map[string]int, len(verdicts))
	for _, v := range verdicts {
		counts[v.Theme]++
	}
	// Sorted keys, not map order: the winner of a tie is discarded either way, but a
	// deterministic scan is what makes that provable in a test.
	themes := make([]string, 0, len(counts))
	for t := range counts {
		themes = append(themes, t)
	}
	sort.Strings(themes)

	best, bestVotes, tied := "", 0, false
	for _, t := range themes {
		switch n := counts[t]; {
		case n > bestVotes:
			best, bestVotes, tied = t, n, false
		case n == bestVotes:
			tied = true
		}
	}
	if tied || best == "" {
		return Verdict{}
	}
	return Verdict{Theme: best, Confidence: float64(bestVotes) / float64(len(verdicts))}
}
