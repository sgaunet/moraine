// Package classify assigns a theme to a cluster using a three-stage pipeline:
// a pure-Go heuristic first, then an optional Ollama vision model constrained to
// the configured theme set, then a guaranteed fallback theme (FR-004/FR-005).
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

// mountainAltitudeM tunes the heuristic threshold.
const mountainAltitudeM = 1500.0

// Options configures the labelling pipeline.
type Options struct {
	Themes     []string   // configured theme slugs
	Fallback   string     // theme used when none is determined
	Classifier Classifier // optional; nil skips the model stage
}

// Label returns a configured theme for the cluster and the Method used. It tries
// the heuristic, then the model (if configured and reachable), then the fallback.
func Label(ctx context.Context, c photo.Cluster, opts Options) (string, Method) {
	if l := heuristic(c, opts.Themes); l != "" {
		return l, MethodHeuristic
	}
	if opts.Classifier != nil {
		if l, err := opts.Classifier.Classify(ctx, c); err == nil {
			if l = strings.TrimSpace(l); l != "" && inSet(l, opts.Themes) {
				return l, modelMethod(c)
			}
		}
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

// heuristic returns "mountain" when a photo is high enough AND "mountain" is a
// configured theme, otherwise "".
func heuristic(c photo.Cluster, themes []string) string {
	if !inSet("mountain", themes) {
		return ""
	}
	for _, p := range c.Photos {
		if p.Altitude != nil && *p.Altitude >= mountainAltitudeM {
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
