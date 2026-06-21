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
