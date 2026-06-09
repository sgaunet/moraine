// Package classify assigns a category/label to a cluster using a three-stage
// pipeline: a pure-Go heuristic first, then an optional Ollama vision model,
// then a date-based fallback that always yields a usable label (FR-004 / R5).
package classify

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/sgaunet/moraine/internal/photo"
)

// Classifier produces a short category for a cluster (implemented by Ollama or
// a fake in tests).
type Classifier interface {
	Classify(ctx context.Context, c photo.Cluster) (string, error)
}

// Options configures the labelling pipeline.
type Options struct {
	Home       *photo.LatLng // optional, for the travel heuristic
	Classifier Classifier    // optional; nil skips the model stage
}

// mountainAltitudeM and travelDistanceKm tune the heuristic thresholds.
const (
	mountainAltitudeM = 1500.0
	travelDistanceKm  = 100.0
)

// Label returns a non-empty label for the cluster. It tries the heuristic, then
// the model (if configured and reachable), then a date-based fallback.
func Label(ctx context.Context, c photo.Cluster, opts Options) string {
	if l := heuristic(c, opts.Home); l != "" {
		return l
	}
	if opts.Classifier != nil {
		if l, err := opts.Classifier.Classify(ctx, c); err == nil {
			if l = strings.TrimSpace(l); l != "" {
				return l
			}
		}
	}
	return DateLabel(c.Start)
}

// DateLabel derives the date-based fallback label (e.g. "2025-08-12").
func DateLabel(t time.Time) string {
	return t.Format("2006-01-02")
}

// heuristic returns a label from metadata alone, or "" if inconclusive.
func heuristic(c photo.Cluster, home *photo.LatLng) string {
	for _, p := range c.Photos {
		if p.Altitude != nil && *p.Altitude >= mountainAltitudeM {
			return "montagne"
		}
	}
	if home != nil {
		if ll, ok := clusterGPS(c); ok && haversineKm(*home, ll) >= travelDistanceKm {
			return "voyage"
		}
	}
	return ""
}

// clusterGPS returns the first available GPS coordinate in the cluster.
func clusterGPS(c photo.Cluster) (photo.LatLng, bool) {
	for _, p := range c.Photos {
		if p.GPS != nil {
			return *p.GPS, true
		}
	}
	return photo.LatLng{}, false
}

// haversineKm returns the great-circle distance between two points in km.
func haversineKm(a, b photo.LatLng) float64 {
	const earthKm = 6371.0
	lat1, lat2 := rad(a.Lat), rad(b.Lat)
	dLat := rad(b.Lat - a.Lat)
	dLng := rad(b.Lng - a.Lng)
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1)*math.Cos(lat2)*math.Sin(dLng/2)*math.Sin(dLng/2)
	return earthKm * 2 * math.Atan2(math.Sqrt(h), math.Sqrt(1-h))
}

func rad(deg float64) float64 { return deg * math.Pi / 180 }
