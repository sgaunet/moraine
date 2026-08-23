// Package classify assigns a theme to a cluster using a three-stage pipeline:
// an optional Ollama vision model (constrained to the configured theme set)
// decides first; if it is unavailable, errors, or abstains, a pure-Go altitude
// heuristic applies; otherwise a guaranteed fallback theme is used
// (FR-004/FR-005).
package classify

import (
	"context"
	"strings"

	"github.com/sgaunet/moraine/internal/photo"
)

// Classifier produces a theme slug for a cluster (implemented by Ollama or a
// fake in tests). It returns "" with a nil error when it cannot decide.
type Classifier interface {
	Classify(ctx context.Context, c photo.Cluster) (string, error)
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
}

// Label returns a configured theme for the cluster and the Method used. The
// model (if configured and reachable) decides first so it sees the actual scene;
// only when it is unavailable, errors, or abstains does the altitude heuristic
// apply, and the fallback theme last.
func Label(ctx context.Context, c photo.Cluster, opts Options) (string, Method) {
	if opts.Classifier != nil {
		if l, err := opts.Classifier.Classify(ctx, c); err == nil {
			if l = strings.TrimSpace(l); l != "" && inSet(l, opts.Themes) {
				return l, modelMethod(c)
			}
		}
	}
	if l := heuristic(c, opts); l != "" {
		return l, MethodHeuristic
	}
	return opts.Fallback, MethodFallback
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
