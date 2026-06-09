package server

import (
	"net/http"

	"github.com/sgaunet/moraine/internal/store"
)

// handleGroups serves the full current state in chronological order (US1).
func (s *Server) handleGroups(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, s.store.Snapshot())
}

// handleThumb serves a photo's thumbnail (or placeholder), with ETag-based
// conditional requests (US1).
func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	id := store.PhotoID(r.PathValue("photoID"))
	ref, ok := s.store.Photo(id)
	if !ok {
		s.writeError(w, http.StatusNotFound, "not_found", "photo inconnue : "+string(id))
		return
	}
	if s.thumbs == nil {
		s.writeError(w, http.StatusInternalServerError, "internal", "générateur de vignettes indisponible")
		return
	}

	data, contentType, etag, err := s.thumbs.Thumbnail(ref.Path, ref.Format)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "thumb_failed",
			"vignette indisponible pour "+ref.Name+" : "+err.Error())
		return
	}

	if etag != "" {
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=300")
	_, _ = w.Write(data)
}
