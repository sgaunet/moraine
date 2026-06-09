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

func TestPhotoEndpointStreamsSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "full.jpg")
	content := []byte("FULL-RESOLUTION-BYTES")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	s := store.New(dir, t.TempDir())
	g := s.AddGroup("g", "g", time.Now(), time.Now(), []photo.Photo{
		{Path: path, Name: "full.jpg", Taken: time.Now(), Format: photo.JPEG},
	})
	id := g.Photos[0].ID

	rec := httptest.NewRecorder()
	newServer(s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/photo/"+string(id), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("content-type = %q; want image/jpeg", ct)
	}
	if rec.Body.String() != string(content) {
		t.Errorf("body = %q; want source bytes", rec.Body.String())
	}
}

func TestPhotoEndpoint404Unknown(t *testing.T) {
	s := store.New(t.TempDir(), t.TempDir())
	rec := httptest.NewRecorder()
	newServer(s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/photo/p999", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404", rec.Code)
	}
}

func TestPhotoEndpoint404MissingFile(t *testing.T) {
	s := store.New(t.TempDir(), t.TempDir())
	g := s.AddGroup("g", "g", time.Now(), time.Now(), []photo.Photo{
		{Path: filepath.Join(t.TempDir(), "gone.jpg"), Name: "gone.jpg", Taken: time.Now(), Format: photo.JPEG},
	})
	rec := httptest.NewRecorder()
	newServer(s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/photo/"+string(g.Photos[0].ID), nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404 for missing source file", rec.Code)
	}
}
