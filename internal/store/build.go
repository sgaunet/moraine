package store

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/sgaunet/moraine/internal/photo"
)

// BuildFromClusters populates the store from classified clusters. labels[i] is
// the category for clusters[i]; a blank label falls back to a date label so a
// group is never unlabelled (VR-5 / FR-004). The destination sub-directory is
// pre-filled as "<label>/<YYYY-MM-DD>" and remains editable.
func BuildFromClusters(s *Store, clusters []photo.Cluster, labels []string) {
	for i, c := range clusters {
		label := ""
		if i < len(labels) {
			label = strings.TrimSpace(labels[i])
		}
		if label == "" {
			label = dateLabel(c.Start)
		}
		subdir := filepath.Join(label, c.Start.Format("2006-01-02"))
		s.AddGroup(label, subdir, c.Start, c.End, c.Photos)
	}
}

// dateLabel derives a date-based label (e.g. "2025-08-12") for a group.
func dateLabel(t time.Time) string {
	return t.Format("2006-01-02")
}
