// Package cluster groups photos into events by scanning capture times and
// opening a new group whenever the gap between consecutive photos exceeds a
// configurable threshold. Complexity is O(n log n) (sort) + O(n) (sweep).
package cluster

import (
	"slices"
	"strings"
	"time"

	"github.com/sgaunet/moraine/internal/photo"
)

// Cluster sorts photos by capture time and splits them into temporal events.
// A new event begins whenever the gap to the previous photo exceeds gap.
//
// The sort is a total order — capture time, then path — so the result depends only
// on the set of photos, never on the order they arrive in. Photos sharing one
// capture time are common (a burst, or a batch dated from one filename day), and
// their order decides which of them gets the un-suffixed name at placement time.
func Cluster(photos []photo.Photo, gap time.Duration) []photo.Cluster {
	if len(photos) == 0 {
		return nil
	}

	sorted := make([]photo.Photo, len(photos))
	copy(sorted, photos)
	slices.SortFunc(sorted, func(a, b photo.Photo) int {
		if c := a.Taken.Compare(b.Taken); c != 0 {
			return c
		}
		return strings.Compare(a.Path, b.Path)
	})

	var clusters []photo.Cluster
	current := []photo.Photo{sorted[0]}
	for i := 1; i < len(sorted); i++ {
		if sorted[i].Taken.Sub(sorted[i-1].Taken) > gap {
			clusters = append(clusters, makeCluster(current))
			current = []photo.Photo{sorted[i]}
			continue
		}
		current = append(current, sorted[i])
	}
	clusters = append(clusters, makeCluster(current))
	return clusters
}

// makeCluster wraps a sorted, non-empty slice with its time bounds.
func makeCluster(photos []photo.Photo) photo.Cluster {
	return photo.Cluster{
		Photos: photos,
		Start:  photos[0].Taken,
		End:    photos[len(photos)-1].Taken,
	}
}
