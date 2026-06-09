package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/photo"
	"github.com/sgaunet/moraine/internal/store"
)

func photosN(n int, base time.Time) []photo.Photo {
	out := make([]photo.Photo, n)
	for i := range out {
		out[i] = photo.Photo{
			Path:   "/src/x.jpg",
			Name:   "x.jpg",
			Taken:  base.Add(time.Duration(i) * time.Minute),
			Format: photo.JPEG,
		}
	}
	return out
}

func TestMovePhotoNominal(t *testing.T) {
	s := store.New("/src", "/dst")
	base := time.Now()
	a := s.AddGroup("A", "A", base, base, photosN(2, base))
	b := s.AddGroup("B", "B", base.Add(time.Hour), base.Add(time.Hour), photosN(1, base.Add(time.Hour)))

	pid := a.Photos[0].ID
	if err := s.MovePhoto(pid, b.ID); err != nil {
		t.Fatalf("MovePhoto: %v", err)
	}

	snap := s.Snapshot()
	counts := map[store.GroupID]int{}
	found := map[store.GroupID]bool{}
	for _, g := range snap.Groups {
		counts[g.ID] = g.Count
		for _, p := range g.Photos {
			if p.ID == pid {
				found[g.ID] = true
			}
		}
	}
	if counts[a.ID] != 1 || counts[b.ID] != 2 {
		t.Fatalf("counts A=%d B=%d; want A=1 B=2", counts[a.ID], counts[b.ID])
	}
	if found[a.ID] || !found[b.ID] {
		t.Errorf("photo %s should be in B only", pid)
	}
}

func TestMovePhotoEmptiesSourceGroup(t *testing.T) {
	s := store.New("/src", "/dst")
	base := time.Now()
	a := s.AddGroup("A", "A", base, base, photosN(1, base)) // single photo
	b := s.AddGroup("B", "B", base.Add(time.Hour), base.Add(time.Hour), photosN(1, base.Add(time.Hour)))

	if err := s.MovePhoto(a.Photos[0].ID, b.ID); err != nil {
		t.Fatalf("MovePhoto: %v", err)
	}
	// Source group A is now empty and must disappear (I3).
	for _, g := range s.Snapshot().Groups {
		if g.ID == a.ID {
			t.Fatalf("emptied group %s should be removed", a.ID)
		}
	}
	if len(s.Snapshot().Groups) != 1 {
		t.Errorf("want 1 group left; got %d", len(s.Snapshot().Groups))
	}
}

func TestMovePhotoUnknownPhoto(t *testing.T) {
	s := store.New("/src", "/dst")
	base := time.Now()
	b := s.AddGroup("B", "B", base, base, photosN(1, base))
	if err := s.MovePhoto("p999", b.ID); !errors.Is(err, store.ErrPhotoNotFound) {
		t.Fatalf("error = %v; want ErrPhotoNotFound", err)
	}
}

func TestMovePhotoUnknownTargetGroup(t *testing.T) {
	s := store.New("/src", "/dst")
	base := time.Now()
	a := s.AddGroup("A", "A", base, base, photosN(1, base))
	if err := s.MovePhoto(a.Photos[0].ID, "g999"); !errors.Is(err, store.ErrGroupNotFound) {
		t.Fatalf("error = %v; want ErrGroupNotFound", err)
	}
}
