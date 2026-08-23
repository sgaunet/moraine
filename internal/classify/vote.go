package classify

import (
	"context"
	"fmt"
	"sort"

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

// classifyByVote asks about each image separately and reduces the answers with
// tallyVotes. A photo whose call fails simply does not vote; only when every one of
// them fails is the whole classification an error, since that means the model was
// unavailable rather than undecided.
func (o *OllamaClassifier) classifyByVote(ctx context.Context, c photo.Cluster, images []string) (Verdict, error) {
	verdicts := make([]Verdict, 0, len(images))
	var lastErr error
	for i, img := range images {
		voteCtx, cancel := o.bounded(ctx)
		v, err := o.ask(voteCtx, c, []string{img})
		cancel()
		if err != nil {
			lastErr = err
			o.log().Debug("vote failed", "vote", i+1, "of", len(images), "err", err)
			continue
		}
		verdicts = append(verdicts, v)
	}
	if len(verdicts) == 0 {
		return Verdict{}, fmt.Errorf("every vote failed: %w", lastErr)
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
