package classify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/sgaunet/moraine/internal/photo"
)

// Describe-then-decide. With a Decider configured, the vision model is not asked for
// a theme: it describes each sampled photo in a sentence or two, and the decision
// service picks the theme from those words. Separate descriptions are what let a
// mixed event show itself — a lunch stop inside a hiking day reads as two different
// scenes — and the decision service answers with a calibrated confidence the vision
// model's self-report is not.
//
// Every failure of the decision half costs that event nothing but the new method:
// the images already sampled go to the single-call classification, exactly as if no
// decider were configured. A decision service that cannot answer at all is found out
// once, by the readiness check, and then left alone for the rest of the run.

// describeSystemPrompt asks for a short, literal account of one photo. Setting,
// subjects and activity are what separate the themes; speculation and lists would
// only spend the decision service's small state budget.
const describeSystemPrompt = "Describe the photo in one or two plain sentences: the setting, " +
	"the main subjects and what they are doing. Say only what is visible. No lists, no guesses."

// describeUserPrompt is the per-photo request.
const describeUserPrompt = "Describe this photo."

// fallbackDescription is what the fallback theme means when it is offered to the
// decision service: choosing it is an abstention.
const fallbackDescription = "the photos fit none of the other themes"

// Description budget, in characters. The decision service's state holds about 768
// tokens (Laya's multilingual checkpoint), shared by every description and the
// capture context; past it the service truncates silently rather than refusing.
const (
	descriptionMaxChars   = 300
	descriptionTotalChars = 2400
)

// readiness is the per-run state of the decision service. It moves from unchecked
// once, and never back.
type readiness int

const (
	unchecked readiness = iota
	ready
	unusable
)

// deciderState guards readiness. Classify is called from one goroutine per run, but
// the lock keeps a caller that does otherwise from checking twice.
type deciderState struct {
	mu    sync.Mutex
	state readiness
}

// decide runs the decision half for an event whose images are sampled and whose
// model is loaded. ok is false when the event should take the single-call path; err
// is set only for an interrupt, which the caller returns as is.
func (o *OllamaClassifier) decide(ctx context.Context, c photo.Cluster, images []sampled) (Verdict, bool, error) {
	usable, err := o.deciderReady(ctx)
	if err != nil || !usable {
		return Verdict{}, false, err
	}
	v, err := o.describeAndDecide(ctx, c, images)
	if err == nil {
		return v, true, nil
	}
	if ctx.Err() != nil {
		// The run's own context, not the error: a timed-out attempt also wraps
		// context.DeadlineExceeded, and that one is a failure, not an interrupt.
		return Verdict{}, false, ctx.Err()
	}
	o.log().Warn("decision failed: this event is classified by the vision model alone", "reason", err)
	return Verdict{}, false, nil
}

// deciderReady runs the readiness check the first time it is asked and reports the
// stored outcome after that, warning once when the service is unusable. An interrupt
// during the check is returned and leaves the state unchecked: the run is ending, and
// "unusable" would be a claim about the service nobody established.
func (o *OllamaClassifier) deciderReady(ctx context.Context) (bool, error) {
	o.decider.mu.Lock()
	defer o.decider.mu.Unlock()
	if o.decider.state != unchecked {
		return o.decider.state == ready, nil
	}
	err := o.Decider.Ready(ctx, Decision{Options: o.decisionOptions(), Fallback: o.Fallback})
	if err != nil && ctx.Err() != nil {
		return false, ctx.Err()
	}
	if err != nil {
		o.decider.state = unusable
		o.log().Warn("decision service unusable: classifying with the vision model alone",
			slices.Concat(o.DeciderAttrs, []any{"reason", err})...)
		return false, nil
	}
	o.decider.state = ready
	o.log().Info("decision service ready", o.DeciderAttrs...)
	return true, nil
}

// describeAndDecide describes the sampled photos and asks the decision service about
// them: once for the event, or once per photo with --vote on a large group.
func (o *OllamaClassifier) describeAndDecide(ctx context.Context, c photo.Cluster, images []sampled) (Verdict, error) {
	descriptions := o.describeAll(ctx, images)
	if len(descriptions) == 0 {
		return Verdict{}, errors.New("the vision model described none of the photos")
	}
	d := Decision{
		Descriptions: descriptions,
		Context:      ContextLines(c),
		Options:      o.decisionOptions(),
		Fallback:     o.Fallback,
	}
	if o.Vote && len(c.Photos) > SmallGroupMax {
		return o.decideByVote(ctx, d)
	}
	v, err := o.Decider.Decide(ctx, d)
	if err != nil {
		return Verdict{}, fmt.Errorf("deciding the theme: %w", err)
	}
	v.Decided = true
	return v, nil
}

// decisionOptions lists the configured themes, then the fallback, each with its
// effective description.
func (o *OllamaClassifier) decisionOptions() []ThemeOption {
	return EffectiveDescriptions(o.Themes, o.Fallback, o.ThemeDescriptions)
}

// decideByVote asks about each description on its own and reduces the answers with
// tallyVotes, the rules --vote applies to the vision model's answers. A photo whose
// decision fails does not vote; only when all of them fail is the event an error.
func (o *OllamaClassifier) decideByVote(ctx context.Context, d Decision) (Verdict, error) {
	votes := make([]vote, len(d.Descriptions))
	o.fanOut(len(d.Descriptions), func(i int) {
		one := d
		one.Descriptions = d.Descriptions[i : i+1]
		v, err := o.Decider.Decide(ctx, one)
		votes[i] = vote{verdict: v, err: err}
	})
	verdicts := make([]Verdict, 0, len(votes))
	var firstErr error
	for i, v := range votes {
		if v.err != nil {
			if firstErr == nil {
				firstErr = v.err
			}
			o.log().Debug("decision vote failed", "vote", i+1, "of", len(votes), "err", v.err)
			continue
		}
		verdicts = append(verdicts, v.verdict)
	}
	if len(verdicts) == 0 {
		return Verdict{}, fmt.Errorf("every decision vote failed: %w", firstErr)
	}
	won := tallyVotes(verdicts)
	won.Decided = true
	o.log().Debug("decision vote result",
		"theme", won.Theme, "confidence", won.Confidence, "votes", len(verdicts), "of", len(votes))
	return won, nil
}

// describeAll describes every sampled image, voteWorkers wide, and returns the
// non-empty descriptions in photo order, each cut to its share of the budget. A photo
// whose description fails is left out, as a failed vote is.
func (o *OllamaClassifier) describeAll(ctx context.Context, images []sampled) []string {
	texts := make([]string, len(images))
	o.fanOut(len(images), func(i int) {
		callCtx, cancel := o.bounded(ctx)
		defer cancel()
		text, err := o.describeOne(callCtx, images[i].data)
		if err != nil {
			o.log().Debug("description failed", "path", images[i].path, "err", err)
			return
		}
		texts[i] = text
	})
	out := make([]string, 0, len(texts))
	for _, t := range texts {
		if t != "" {
			out = append(out, t)
		}
	}
	budget := min(descriptionMaxChars, descriptionTotalChars/max(len(out), 1))
	for i := range out {
		out[i] = cutRunes(out[i], budget)
	}
	for i, img := range images {
		if texts[i] != "" {
			o.log().Debug("description", "path", img.path, "text", cutRunes(texts[i], budget))
		}
	}
	return out
}

// describeOne asks the vision model for one photo's description, retrying only a
// transient failure.
func (o *OllamaClassifier) describeOne(ctx context.Context, image string) (string, error) {
	payload, err := json.Marshal(chatRequest{
		Model:     o.Model,
		Options:   chatOptions{Temperature: 0, Seed: ollamaSeed},
		KeepAlive: o.KeepAlive,
		Messages: []chatMessage{
			{Role: "system", Content: describeSystemPrompt},
			{Role: "user", Content: describeUserPrompt, Images: []string{image}},
		},
	})
	if err != nil {
		return "", fmt.Errorf("encoding ollama describe request: %w", err)
	}
	body, _, err := o.postChatRetrying(ctx, payload)
	if err != nil {
		return "", err
	}
	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("unreadable ollama response: %w", err)
	}
	return strings.TrimSpace(parsed.Message.Content), nil
}

// fanOut runs work(i) for every i in [0, n), at most voteWorkers(n) at once, and
// waits for all of them. Each call owns slot i of whatever it writes, so the results
// come back in input order with no lock.
func (o *OllamaClassifier) fanOut(n int, work func(i int)) {
	if n == 0 {
		return
	}
	sem := make(chan struct{}, o.voteWorkers(n))
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			work(i)
		}()
	}
	wg.Wait()
}

// cutRunes shortens s to at most n characters without splitting one.
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}
