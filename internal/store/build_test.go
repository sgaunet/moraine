package store_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/photo"
	"github.com/sgaunet/moraine/internal/store"
)

func clusterAt(start time.Time, n int) photo.Cluster {
	photos := make([]photo.Photo, n)
	for i := range photos {
		photos[i] = photo.Photo{
			Path:   "/src/p.jpg",
			Name:   "p.jpg",
			Taken:  start.Add(time.Duration(i) * time.Minute),
			Format: photo.JPEG,
		}
	}
	return photo.Cluster{Photos: photos, Start: start, End: start.Add(time.Duration(n) * time.Minute)}
}

func TestBuildFromClustersAssignsIDsAndDest(t *testing.T) {
	s := store.New("/src", "/dst")
	start := time.Date(2025, 8, 12, 8, 0, 0, 0, time.UTC)
	clusters := []photo.Cluster{clusterAt(start, 2)}
	labels := []string{"sortie montagne"}

	store.BuildFromClusters(s, clusters, labels)

	snap := s.Snapshot()
	if len(snap.Groups) != 1 {
		t.Fatalf("got %d groups; want 1", len(snap.Groups))
	}
	g := snap.Groups[0]
	if g.Label != "sortie montagne" {
		t.Errorf("label = %q", g.Label)
	}
	wantDest := filepath.Join("sortie montagne", "2025-08-12")
	if g.DestSubdir != wantDest {
		t.Errorf("dest_subdir = %q; want %q (<label>/<date>)", g.DestSubdir, wantDest)
	}
	if g.ID == "" || len(g.Photos) != 2 {
		t.Errorf("group not fully populated: %+v", g)
	}
	for _, p := range g.Photos {
		if p.ID == "" {
			t.Error("photo without an allocated ID")
		}
	}
}

func TestBuildFromClustersEmptyLabelFallsBackToDate(t *testing.T) {
	s := store.New("/src", "/dst")
	start := time.Date(2025, 3, 9, 12, 0, 0, 0, time.UTC)
	clusters := []photo.Cluster{clusterAt(start, 1)}

	// Blank label must become the date label (VR-5 / FR-004).
	store.BuildFromClusters(s, clusters, []string{"   "})

	g := s.Snapshot().Groups[0]
	if g.Label != "2025-03-09" {
		t.Errorf("label = %q; want date fallback 2025-03-09", g.Label)
	}
	if g.DestSubdir != filepath.Join("2025-03-09", "2025-03-09") {
		t.Errorf("dest_subdir = %q", g.DestSubdir)
	}
}

func TestBuildFromClustersMissingLabelsSlice(t *testing.T) {
	s := store.New("/src", "/dst")
	start := time.Date(2025, 3, 9, 12, 0, 0, 0, time.UTC)
	clusters := []photo.Cluster{clusterAt(start, 1)}

	// Fewer labels than clusters → date fallback, never empty.
	store.BuildFromClusters(s, clusters, nil)
	if g := s.Snapshot().Groups[0]; g.Label == "" {
		t.Error("label must never be empty")
	}
}
