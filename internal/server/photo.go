package server

import (
	"net/http"
	"os"

	"github.com/sgaunet/moraine/internal/photo"
	"github.com/sgaunet/moraine/internal/store"
)

// handlePhoto streams the full-resolution source image for preview (US5).
func (s *Server) handlePhoto(w http.ResponseWriter, r *http.Request) {
	id := store.PhotoID(r.PathValue("photoID"))
	ref, ok := s.store.Photo(id)
	if !ok {
		s.writeError(w, http.StatusNotFound, "not_found", "photo inconnue : "+string(id))
		return
	}

	f, err := os.Open(ref.Path)
	if err != nil {
		s.writeError(w, http.StatusNotFound, "not_found",
			"fichier source introuvable pour "+ref.Name)
		return
	}
	defer func() { _ = f.Close() }()

	w.Header().Set("Content-Type", contentTypeFor(ref.Format))
	// ServeContent handles Range requests and conditional headers.
	http.ServeContent(w, r, ref.Name, ref.Taken, f)
}

func contentTypeFor(format photo.Format) string {
	switch format {
	case photo.JPEG:
		return "image/jpeg"
	case photo.PNG:
		return "image/png"
	case photo.HEIC:
		return "image/heic"
	default:
		return "application/octet-stream"
	}
}
