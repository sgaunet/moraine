package server_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/photo"
	"github.com/sgaunet/moraine/internal/server"
	"github.com/sgaunet/moraine/internal/store"
)

// fakeThumb is a deterministic Thumbnailer (no disk, stable ETag per path).
type fakeThumb struct{}

func (fakeThumb) Thumbnail(path string, _ photo.Format) ([]byte, string, string, error) {
	return []byte("THUMB:" + path), "image/jpeg", `"etag-` + path + `"`, nil
}

func newServer(st *store.Store) http.Handler {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return server.New(st, fakeThumb{}, log).Handler()
}

// seeded returns a store with one group of two photos pointing at real files.
func seeded(t *testing.T) (*store.Store, store.GroupID, []store.PhotoID) {
	t.Helper()
	s := store.New(t.TempDir(), t.TempDir())
	base := time.Date(2025, 8, 12, 8, 0, 0, 0, time.UTC)
	g := s.AddGroup("sortie", "sortie/2025-08-12", base, base, []photo.Photo{
		{Path: "/src/a.jpg", Name: "a.jpg", Taken: base, Format: photo.JPEG},
		{Path: "/src/b.jpg", Name: "b.jpg", Taken: base.Add(time.Minute), Format: photo.JPEG},
	})
	ids := make([]store.PhotoID, 0, 2)
	for _, p := range g.Photos {
		ids = append(ids, p.ID)
	}
	return s, g.ID, ids
}

func decodeError(t *testing.T, body io.Reader) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(body).Decode(&m); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return m
}

func TestIndexServesHTML(t *testing.T) {
	s := store.New(t.TempDir(), t.TempDir())
	rec := httptest.NewRecorder()
	newServer(s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q; want text/html", ct)
	}
	if !strings.Contains(rec.Body.String(), "<!DOCTYPE html>") {
		t.Error("index body should be the embedded HTML")
	}
}

func TestUnknownRouteReturnsJSON404(t *testing.T) {
	s := store.New(t.TempDir(), t.TempDir())
	rec := httptest.NewRecorder()
	newServer(s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/does-not-exist", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %q; want application/json", ct)
	}
	m := decodeError(t, rec.Body)
	if m["error"] == "" || m["message"] == "" {
		t.Errorf("error envelope must have non-empty error+message; got %v", m)
	}
}

func TestAssetServed(t *testing.T) {
	s := store.New(t.TempDir(), t.TempDir())
	rec := httptest.NewRecorder()
	newServer(s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 for /assets/app.js", rec.Code)
	}
}
