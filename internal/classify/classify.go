// Package classify assigns a theme to a cluster using a three-stage pipeline:
// an optional Ollama vision model (constrained to the configured theme set)
// decides first; if it is unavailable, errors, abstains, or is not confident
// enough (Options.MinConfidence), a pure-Go altitude heuristic applies; otherwise
// a guaranteed fallback theme is used (FR-004/FR-005).
package classify

import (
	"context"
	"strings"

	"github.com/sgaunet/moraine/internal/photo"
)

// Verdict is what a Classifier decided about a cluster.
type Verdict struct {
	// Theme is a configured theme slug, or "" when the classifier abstained — it
	// looked and had no opinion, which is different from failing.
	Theme string
	// Confidence is how sure the classifier is, from 0 to 1. Zero means "not
	// reported": a single model call carries whatever confidence the model puts in
	// its answer, a vote carries the share of votes the winning theme took, and a
	// model that answers neither leaves it at 0. A verdict with no reported
	// confidence is never rejected by MinConfidence — see MeetsThreshold.
	Confidence float64
}

// MeetsThreshold reports whether the verdict is confident enough to be used.
//
// A threshold of 0 — the default — accepts everything, which is what keeps the gate
// off until a threshold has been chosen from measured data (see the eval harness)
// rather than guessed at. A verdict carrying no confidence at all is also accepted:
// a model that ignores the confidence field is not thereby telling us it was unsure,
// and rejecting those would send an entire run to the fallback theme.
func (v Verdict) MeetsThreshold(threshold float64) bool {
	if threshold <= 0 || v.Confidence == 0 {
		return true
	}
	return v.Confidence >= threshold
}

// Classifier produces a Verdict for a cluster (implemented by Ollama or a fake in
// tests). An abstention is a zero-Theme Verdict with a nil error; a failure is an
// error.
type Classifier interface {
	Classify(ctx context.Context, c photo.Cluster) (Verdict, error)
}

// Method records how a cluster's theme was decided (for logging, SC-005).
type Method string

const (
	// MethodHeuristic — decided by metadata alone (altitude → mountain).
	MethodHeuristic Method = "heuristic"
	// MethodModelAll — the model classified a small group using all its photos (≤3).
	MethodModelAll Method = "model-all"
	// MethodModelSample — the model classified a large group from a sample (>3).
	MethodModelSample Method = "model-sample"
	// MethodFallback — no theme was determined; the fallback theme was used.
	MethodFallback Method = "fallback"
	// MethodManifest — the theme an earlier run already filed this event under was
	// reused (an incremental run), so no classifier ran at all.
	MethodManifest Method = "manifest"
)

// SmallGroupMax is the largest group size still classified using all photos.
const SmallGroupMax = 3

// Options configures the labelling pipeline.
type Options struct {
	Themes     []string   // configured theme slugs
	Fallback   string     // theme used when none is determined
	Classifier Classifier // optional; nil skips the model stage
	// MountainAltitudeM is the altitude in metres at or above which the
	// heuristic labels a group "mountain". A non-positive value disables the
	// altitude heuristic (see heuristic) rather than matching every photo.
	MountainAltitudeM float64
	// MinConfidence is the confidence a model verdict must reach to be used, from 0
	// to 1. Zero (the default) accepts every verdict, so the gate costs nothing
	// until a threshold has been measured. Below it, the cluster falls through to
	// the heuristic and then the fallback theme, exactly as an abstention does.
	MinConfidence float64
}

// Label returns a configured theme for the cluster and the Method used. The
// model (if configured and reachable) decides first so it sees the actual scene;
// only when it is unavailable, errors, abstains, or answers below
// opts.MinConfidence does the altitude heuristic apply, and the fallback theme
// last.
func Label(ctx context.Context, c photo.Cluster, opts Options) (string, Method) {
	if theme := modelTheme(ctx, c, opts); theme != "" {
		return theme, modelMethod(c)
	}
	if l := heuristic(c, opts); l != "" {
		return l, MethodHeuristic
	}
	return opts.Fallback, MethodFallback
}

// modelTheme returns the model's answer when there is a usable one, else "". Every
// way of not having one — no classifier, a failure, an abstention, a theme outside
// the configured set, or a confidence below MinConfidence — reads the same to the
// caller, which is what lets Label treat them all as "ask the heuristic next".
func modelTheme(ctx context.Context, c photo.Cluster, opts Options) string {
	if opts.Classifier == nil {
		return ""
	}
	v, err := opts.Classifier.Classify(ctx, c)
	if err != nil {
		return ""
	}
	theme := strings.TrimSpace(v.Theme)
	if theme == "" || !inSet(theme, opts.Themes) {
		return ""
	}
	if !v.MeetsThreshold(opts.MinConfidence) {
		return ""
	}
	return theme
}

// modelMethod reports whether the model saw all photos (≤3) or a sample (>3).
func modelMethod(c photo.Cluster) Method {
	if len(c.Photos) <= SmallGroupMax {
		return MethodModelAll
	}
	return MethodModelSample
}

// heuristic returns "mountain" when a photo is at or above
// opts.MountainAltitudeM AND "mountain" is a configured theme, otherwise "". It
// runs only as a fallback after the model (see Label), so it no longer pre-empts
// the model — chiefly it keeps the offline run (no Ollama) useful for clearly
// high-altitude clusters.
//
// A non-positive threshold disables the check: callers that leave it unset must
// not silently get "every photo is a mountain". config.New guarantees a positive
// value for a real run.
func heuristic(c photo.Cluster, opts Options) string {
	if opts.MountainAltitudeM <= 0 || !inSet("mountain", opts.Themes) {
		return ""
	}
	for _, p := range c.Photos {
		if p.Altitude != nil && *p.Altitude >= opts.MountainAltitudeM {
			return "mountain"
		}
	}
	return ""
}

// inSet reports whether slug is one of the configured themes.
func inSet(slug string, themes []string) bool {
	for _, t := range themes {
		if t == slug {
			return true
		}
	}
	return false
}
