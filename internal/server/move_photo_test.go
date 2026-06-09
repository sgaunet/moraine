package server_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/photo"
	"github.com/sgaunet/moraine/internal/store"
)

func TestMovePhotoHandler200(t *testing.T) {
	s := store.New(t.TempDir(), t.TempDir())
	base := time.Now()
	a := s.AddGroup("A", "A", base, base, []photo.Photo{
		{Name: "a.jpg", Taken: base, Format: photo.JPEG},
		{Name: "a2.jpg", Taken: base, Format: photo.JPEG},
	})
	b := s.AddGroup("B", "B", base.Add(time.Hour), base.Add(time.Hour), []photo.Photo{
		{Name: "b.jpg", Taken: base.Add(time.Hour), Format: photo.JPEG},
	})

	body := strings.NewReader(`{"to_group":"` + string(b.ID) + `"}`)
	rec := httptest.NewRecorder()
	newServer(s).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/photos/"+string(a.Photos[0].ID)+"/move", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200\nbody=%s", rec.Code, rec.Body.String())
	}
	m := decodeError(t, rec.Body)
	if m["ok"] != true {
		t.Errorf("body = %v; want {ok:true}", m)
	}
}

func TestMovePhotoHandler400(t *testing.T) {
	s, _, ids := seeded(t)
	h := newServer(s)

	// Missing to_group.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/photos/"+string(ids[0])+"/move", strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 for missing to_group", rec.Code)
	}

	// Malformed JSON.
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodPost, "/api/photos/"+string(ids[0])+"/move", strings.NewReader(`{bad`)))
	if rec2.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 for malformed JSON", rec2.Code)
	}
}

func TestMovePhotoHandler404(t *testing.T) {
	s, gid, ids := seeded(t)
	h := newServer(s)

	// Unknown photo.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/photos/p999/move", strings.NewReader(`{"to_group":"`+string(gid)+`"}`)))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404 for unknown photo", rec.Code)
	}

	// Unknown target group.
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodPost, "/api/photos/"+string(ids[0])+"/move", strings.NewReader(`{"to_group":"g999"}`)))
	if rec2.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404 for unknown target group", rec2.Code)
	}
}
