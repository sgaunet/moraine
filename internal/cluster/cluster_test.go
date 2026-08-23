package cluster_test

import (
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/cluster"
	"github.com/sgaunet/moraine/internal/photo"
)

func at(base time.Time, mins ...int) []photo.Photo {
	out := make([]photo.Photo, len(mins))
	for i, m := range mins {
		out[i] = photo.Photo{Taken: base.Add(time.Duration(m) * time.Minute)}
	}
	return out
}

func TestClusterGapSplitting(t *testing.T) {
	base := time.Date(2025, 8, 12, 8, 0, 0, 0, time.UTC)
	gap := 4 * time.Hour

	tests := []struct {
		name      string
		mins      []int
		wantSizes []int
	}{
		{"empty", nil, nil},
		{"single", []int{0}, []int{1}},
		{"all within gap", []int{0, 60, 120, 180}, []int{4}},
		{"one big gap", []int{0, 30, 30 + 300, 30 + 360}, []int{2, 2}},
		{"exactly gap stays together", []int{0, 240}, []int{2}},
		{"just over gap splits", []int{0, 241}, []int{1, 1}},
		{"unsorted input", []int{300, 0, 60}, []int{3}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := cluster.Cluster(at(base, tc.mins...), gap)
			if len(got) != len(tc.wantSizes) {
				t.Fatalf("got %d clusters; want %d (%v)", len(got), len(tc.wantSizes), tc.wantSizes)
			}
			for i, c := range got {
				if len(c.Photos) != tc.wantSizes[i] {
					t.Errorf("cluster %d size = %d; want %d", i, len(c.Photos), tc.wantSizes[i])
				}
			}
		})
	}
}

func TestClusterBounds(t *testing.T) {
	base := time.Date(2025, 8, 12, 8, 0, 0, 0, time.UTC)
	got := cluster.Cluster(at(base, 0, 30, 90), 4*time.Hour)
	if len(got) != 1 {
		t.Fatalf("got %d clusters; want 1", len(got))
	}
	c := got[0]
	if !c.Start.Equal(base) {
		t.Errorf("Start = %v; want %v", c.Start, base)
	}
	if !c.End.Equal(base.Add(90 * time.Minute)) {
		t.Errorf("End = %v; want %v", c.End, base.Add(90*time.Minute))
	}
}

func TestClusterSortsWithinCluster(t *testing.T) {
	base := time.Date(2025, 8, 12, 8, 0, 0, 0, time.UTC)
	got := cluster.Cluster(at(base, 120, 0, 60), 4*time.Hour)
	c := got[0]
	for i := 1; i < len(c.Photos); i++ {
		if c.Photos[i].Taken.Before(c.Photos[i-1].Taken) {
			t.Fatalf("photos not sorted ascending: %v", c.Photos)
		}
	}
}

// TestClusterOrdersEqualTimesByPath pins the total order. Photos sharing one capture
// time are ordinary — a burst, or a batch of downloads dated from one filename day —
// and their order decides which of them keeps the un-suffixed name when they collide
// at placement time. Ordering on capture time alone left that to whichever EXIF
// worker happened to finish first.
func TestClusterOrdersEqualTimesByPath(t *testing.T) {
	taken := time.Date(2025, 8, 12, 8, 0, 0, 0, time.UTC)
	mk := func(paths ...string) []photo.Photo {
		out := make([]photo.Photo, len(paths))
		for i, p := range paths {
			out[i] = photo.Photo{Path: p, Name: p, Taken: taken}
		}
		return out
	}

	forward := cluster.Cluster(mk("a.jpg", "b.jpg", "c.jpg"), time.Hour)
	reverse := cluster.Cluster(mk("c.jpg", "b.jpg", "a.jpg"), time.Hour)
	if len(forward) != 1 || len(reverse) != 1 {
		t.Fatalf("want one cluster each, got %d and %d", len(forward), len(reverse))
	}
	want := []string{"a.jpg", "b.jpg", "c.jpg"}
	for _, got := range []photo.Cluster{forward[0], reverse[0]} {
		var paths []string
		for _, p := range got.Photos {
			paths = append(paths, p.Path)
		}
		if len(paths) != len(want) {
			t.Fatalf("paths = %v; want %v", paths, want)
		}
		for i := range want {
			if paths[i] != want[i] {
				t.Fatalf("paths = %v; want %v (input order must not matter)", paths, want)
			}
		}
	}
}
