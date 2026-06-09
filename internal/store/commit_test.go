package store_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/photo"
	"github.com/sgaunet/moraine/internal/store"
)

// fixtureFile creates a real source file and returns a photo.Photo for it.
func fixtureFile(t *testing.T, dir, name, content string) photo.Photo {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return photo.Photo{Path: path, Name: name, Taken: time.Now(), Format: photo.JPEG}
}

func TestCommitTotalSuccess(t *testing.T) {
	srcDir := t.TempDir()
	destRoot := t.TempDir()
	s := store.New(srcDir, destRoot)

	p1 := fixtureFile(t, srcDir, "a.jpg", "A")
	p2 := fixtureFile(t, srcDir, "b.jpg", "B")
	g := s.AddGroup("voyage", "voyage/2025-08-12", time.Now(), time.Now(), []photo.Photo{p1, p2})

	res, err := s.Commit(g.ID)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if res.Moved != 2 || len(res.Failed) != 0 {
		t.Fatalf("res = %+v; want 2 moved, 0 failed", res)
	}
	// Files at destination, absent from source (SC-002).
	for _, name := range []string{"a.jpg", "b.jpg"} {
		if _, err := os.Stat(filepath.Join(res.Dest, name)); err != nil {
			t.Errorf("expected %s in destination: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(srcDir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s should be gone from source", name)
		}
	}
	// Group removed from state (I1).
	if len(s.Snapshot().Groups) != 0 {
		t.Error("group should be removed after total commit")
	}
}

func TestCommitPartialFailureKeepsFailures(t *testing.T) {
	srcDir := t.TempDir()
	destRoot := t.TempDir()
	s := store.New(srcDir, destRoot)

	good := fixtureFile(t, srcDir, "good.jpg", "G")
	// A photo whose source file does not exist → its move fails.
	missing := photo.Photo{
		Path:   filepath.Join(srcDir, "missing.jpg"),
		Name:   "missing.jpg",
		Taken:  time.Now(),
		Format: photo.JPEG,
	}
	g := s.AddGroup("evt", "evt/2025-01-01", time.Now(), time.Now(), []photo.Photo{good, missing})

	res, err := s.Commit(g.ID)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if res.Moved != 1 || len(res.Failed) != 1 {
		t.Fatalf("res = %+v; want 1 moved, 1 failed (207)", res)
	}
	if res.Failed[0].Name != "missing.jpg" || res.Failed[0].Error == "" {
		t.Errorf("failed entry = %+v; want actionable error for missing.jpg", res.Failed[0])
	}
	// Group survives, holding only the failed photo (I5).
	snap := s.Snapshot()
	if len(snap.Groups) != 1 || snap.Groups[0].Count != 1 {
		t.Fatalf("snapshot = %+v; want group kept with 1 failed photo", snap.Groups)
	}
	if snap.Groups[0].Photos[0].Name != "missing.jpg" {
		t.Errorf("kept photo = %q; want missing.jpg", snap.Groups[0].Photos[0].Name)
	}
	// The good file did move out.
	if _, err := os.Stat(filepath.Join(srcDir, "good.jpg")); !errors.Is(err, os.ErrNotExist) {
		t.Error("good.jpg should have moved out of source")
	}
}

func TestCommitInvalidDestSubdirMovesNothing(t *testing.T) {
	srcDir := t.TempDir()
	destRoot := t.TempDir()
	s := store.New(srcDir, destRoot)

	p := fixtureFile(t, srcDir, "x.jpg", "X")
	g := s.AddGroup("evt", "../escape", time.Now(), time.Now(), []photo.Photo{p})

	_, err := s.Commit(g.ID)
	if !errors.Is(err, store.ErrInvalidDestSubdir) {
		t.Fatalf("Commit error = %v; want ErrInvalidDestSubdir", err)
	}
	// No file moved; source intact (VR-1).
	if _, statErr := os.Stat(filepath.Join(srcDir, "x.jpg")); statErr != nil {
		t.Error("source file should be untouched on invalid dest")
	}
	if len(s.Snapshot().Groups) != 1 {
		t.Error("group should remain after a rejected commit")
	}
}

func TestCommitUnknownGroup(t *testing.T) {
	s := store.New(t.TempDir(), t.TempDir())
	if _, err := s.Commit("g999"); !errors.Is(err, store.ErrGroupNotFound) {
		t.Fatalf("Commit unknown group error = %v; want ErrGroupNotFound", err)
	}
}

func TestCommitNeverOverwrites(t *testing.T) {
	srcDir := t.TempDir()
	destRoot := t.TempDir()
	s := store.New(srcDir, destRoot)

	// Pre-existing file at destination with the same name (I4).
	destSub := filepath.Join(destRoot, "evt", "2025-01-01")
	if err := os.MkdirAll(destSub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destSub, "IMG.jpg"), []byte("ORIGINAL"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := fixtureFile(t, srcDir, "IMG.jpg", "NEW")
	g := s.AddGroup("evt", filepath.Join("evt", "2025-01-01"), time.Now(), time.Now(), []photo.Photo{p})

	res, err := s.Commit(g.ID)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if res.Moved != 1 {
		t.Fatalf("moved = %d; want 1", res.Moved)
	}
	// Original preserved, new file got a unique suffix.
	original, _ := os.ReadFile(filepath.Join(destSub, "IMG.jpg"))
	if string(original) != "ORIGINAL" {
		t.Errorf("original overwritten: %q", original)
	}
	suffixed, err := os.ReadFile(filepath.Join(destSub, "IMG (1).jpg"))
	if err != nil || string(suffixed) != "NEW" {
		t.Errorf("expected 'IMG (1).jpg' with NEW content; got %q err %v", suffixed, err)
	}
}
