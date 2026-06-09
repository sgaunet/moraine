package server_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func patch(t *testing.T, h http.Handler, gid, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/api/groups/"+gid, strings.NewReader(body)))
	return rec
}

func TestPatchGroupLabel(t *testing.T) {
	s, gid, _ := seeded(t)
	h := newServer(s)

	rec := patch(t, h, string(gid), `{"label":"anniversaire"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200\nbody=%s", rec.Code, rec.Body.String())
	}
	if s.Snapshot().Groups[0].Label != "anniversaire" {
		t.Error("label not updated")
	}
}

func TestPatchGroupDestSubdir(t *testing.T) {
	s, gid, _ := seeded(t)
	rec := patch(t, newServer(s), string(gid), `{"dest_subdir":"voyage/italie"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	if s.Snapshot().Groups[0].DestSubdir != "voyage/italie" {
		t.Error("dest_subdir not updated")
	}
}

func TestPatchGroup404(t *testing.T) {
	s, _, _ := seeded(t)
	rec := patch(t, newServer(s), "g999", `{"label":"x"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404", rec.Code)
	}
}

func TestPatchGroup422InvalidDest(t *testing.T) {
	s, gid, _ := seeded(t)
	rec := patch(t, newServer(s), string(gid), `{"dest_subdir":"../escape"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; want 422\nbody=%s", rec.Code, rec.Body.String())
	}
	m := decodeError(t, rec.Body)
	if m["error"] != "invalid_dest_subdir" {
		t.Errorf("error = %v; want invalid_dest_subdir", m["error"])
	}
}

func TestPatchGroupEmptyBody(t *testing.T) {
	s, gid, _ := seeded(t)
	rec := patch(t, newServer(s), string(gid), `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 when no fields provided", rec.Code)
	}
}
