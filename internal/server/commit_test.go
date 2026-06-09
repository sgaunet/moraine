package server_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/photo"
	"github.com/sgaunet/moraine/internal/store"
)

func realPhoto(t *testing.T, dir, name string) photo.Photo {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
		t.Fatal(err)
	}
	return photo.Photo{Path: path, Name: name, Taken: time.Now(), Format: photo.JPEG}
}

func TestCommitHandler200(t *testing.T) {
	srcDir := t.TempDir()
	destRoot := t.TempDir()
	s := store.New(srcDir, destRoot)
	g := s.AddGroup("evt", "evt/2025", time.Now(), time.Now(), []photo.Photo{
		realPhoto(t, srcDir, "a.jpg"),
		realPhoto(t, srcDir, "b.jpg"),
	})

	rec := httptest.NewRecorder()
	newServer(s).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/groups/"+string(g.ID)+"/commit", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200\nbody=%s", rec.Code, rec.Body.String())
	}
	m := decodeError(t, rec.Body)
	if int(m["moved"].(float64)) != 2 || m["dest"] == "" {
		t.Errorf("commit result = %v; want moved=2 + dest", m)
	}
}

func TestCommitHandler207Partial(t *testing.T) {
	srcDir := t.TempDir()
	destRoot := t.TempDir()
	s := store.New(srcDir, destRoot)
	g := s.AddGroup("evt", "evt/2025", time.Now(), time.Now(), []photo.Photo{
		realPhoto(t, srcDir, "good.jpg"),
		{Path: filepath.Join(srcDir, "missing.jpg"), Name: "missing.jpg", Taken: time.Now(), Format: photo.JPEG},
	})

	rec := httptest.NewRecorder()
	newServer(s).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/groups/"+string(g.ID)+"/commit", nil))
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d; want 207\nbody=%s", rec.Code, rec.Body.String())
	}
	m := decodeError(t, rec.Body)
	if int(m["moved"].(float64)) != 1 {
		t.Errorf("moved = %v; want 1", m["moved"])
	}
	failed, ok := m["failed"].([]any)
	if !ok || len(failed) != 1 {
		t.Errorf("failed = %v; want 1 entry", m["failed"])
	}
}

func TestCommitHandler404(t *testing.T) {
	s := store.New(t.TempDir(), t.TempDir())
	rec := httptest.NewRecorder()
	newServer(s).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/groups/g999/commit", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404", rec.Code)
	}
}

func TestCommitHandler422InvalidDest(t *testing.T) {
	srcDir := t.TempDir()
	s := store.New(srcDir, t.TempDir())
	g := s.AddGroup("evt", "../escape", time.Now(), time.Now(), []photo.Photo{
		realPhoto(t, srcDir, "a.jpg"),
	})

	rec := httptest.NewRecorder()
	newServer(s).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/groups/"+string(g.ID)+"/commit", nil))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; want 422\nbody=%s", rec.Code, rec.Body.String())
	}
	m := decodeError(t, rec.Body)
	if m["error"] != "invalid_dest_subdir" {
		t.Errorf("error code = %v; want invalid_dest_subdir", m["error"])
	}
}
