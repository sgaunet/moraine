package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/photo"
	"github.com/sgaunet/moraine/internal/store"
)

func single(t *testing.T) (*store.Store, store.GroupID) {
	t.Helper()
	s := store.New("/src", "/dst")
	base := time.Now()
	g := s.AddGroup("orig", "orig/2025-01-01", base, base, []photo.Photo{
		{Path: "/src/a.jpg", Name: "a.jpg", Taken: base, Format: photo.JPEG},
	})
	return s, g.ID
}

func TestSetLabel(t *testing.T) {
	s, id := single(t)
	if err := s.SetLabel(id, "anniversaire"); err != nil {
		t.Fatalf("SetLabel: %v", err)
	}
	if got := s.Snapshot().Groups[0].Label; got != "anniversaire" {
		t.Errorf("label = %q; want anniversaire", got)
	}
}

func TestSetLabelRejectsEmpty(t *testing.T) {
	s, id := single(t)
	if err := s.SetLabel(id, "   "); !errors.Is(err, store.ErrEmptyLabel) {
		t.Fatalf("error = %v; want ErrEmptyLabel (VR-5)", err)
	}
}

func TestSetLabelUnknownGroup(t *testing.T) {
	s, _ := single(t)
	if err := s.SetLabel("g999", "x"); !errors.Is(err, store.ErrGroupNotFound) {
		t.Fatalf("error = %v; want ErrGroupNotFound", err)
	}
}

func TestSetDestSubdirValid(t *testing.T) {
	s, id := single(t)
	if err := s.SetDestSubdir(id, "voyage/italie"); err != nil {
		t.Fatalf("SetDestSubdir: %v", err)
	}
	if got := s.Snapshot().Groups[0].DestSubdir; got != "voyage/italie" {
		t.Errorf("dest_subdir = %q; want voyage/italie", got)
	}
}

func TestSetDestSubdirRejectsTraversal(t *testing.T) {
	s, id := single(t)
	for _, bad := range []string{"../escape", "../../etc", "a/../../b"} {
		if err := s.SetDestSubdir(id, bad); !errors.Is(err, store.ErrInvalidDestSubdir) {
			t.Errorf("SetDestSubdir(%q) error = %v; want ErrInvalidDestSubdir", bad, err)
		}
	}
	// Unchanged after rejection.
	if got := s.Snapshot().Groups[0].DestSubdir; got != "orig/2025-01-01" {
		t.Errorf("dest_subdir mutated to %q after rejection", got)
	}
}

func TestSetDestSubdirUnknownGroup(t *testing.T) {
	s, _ := single(t)
	if err := s.SetDestSubdir("g999", "ok"); !errors.Is(err, store.ErrGroupNotFound) {
		t.Fatalf("error = %v; want ErrGroupNotFound", err)
	}
}
