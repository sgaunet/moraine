package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/photo"
	"github.com/sgaunet/moraine/internal/store"
)

func TestGroupsEndpointContractAndOrder(t *testing.T) {
	s := store.New(t.TempDir(), t.TempDir())
	t1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC)
	// Insert out of order; response must be chronological.
	s.AddGroup("late", "late", t2, t2, []photo.Photo{{Name: "z.jpg", Taken: t2, Format: photo.JPEG}})
	s.AddGroup("early", "early", t1, t1, []photo.Photo{{Name: "a.jpg", Taken: t1, Format: photo.JPEG}})

	rec := httptest.NewRecorder()
	newServer(s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/groups", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}

	var resp struct {
		Groups []struct {
			ID         string `json:"id"`
			Label      string `json:"label"`
			DestSubdir string `json:"dest_subdir"`
			Count      int    `json:"count"`
			Photos     []struct {
				ID       string `json:"id"`
				ThumbURL string `json:"thumb_url"`
				PhotoURL string `json:"photo_url"`
			} `json:"photos"`
		} `json:"groups"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Groups) != 2 {
		t.Fatalf("got %d groups; want 2", len(resp.Groups))
	}
	if resp.Groups[0].Label != "early" || resp.Groups[1].Label != "late" {
		t.Errorf("order = [%s, %s]; want [early, late]", resp.Groups[0].Label, resp.Groups[1].Label)
	}
	p := resp.Groups[0].Photos[0]
	if p.ThumbURL != "/thumb/"+p.ID || p.PhotoURL != "/photo/"+p.ID {
		t.Errorf("derived URLs wrong: %+v", p)
	}
}

func TestThumbEndpoint(t *testing.T) {
	s, _, ids := seeded(t)
	h := newServer(s)

	// 200 + ETag.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/thumb/"+string(ids[0]), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}

	// 304 when If-None-Match matches.
	rec2 := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/thumb/"+string(ids[0]), nil)
	req.Header.Set("If-None-Match", etag)
	h.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusNotModified {
		t.Errorf("status = %d; want 304 on matching ETag", rec2.Code)
	}

	// 404 for unknown photo.
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, httptest.NewRequest(http.MethodGet, "/thumb/p999", nil))
	if rec3.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404 for unknown photo", rec3.Code)
	}
}
