package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/sgaunet/moraine/internal/store"
)

type movePhotoRequest struct {
	ToGroup string `json:"to_group"`
}

// handleMovePhoto moves a photo between groups (logical only, no disk effect, US3).
func (s *Server) handleMovePhoto(w http.ResponseWriter, r *http.Request) {
	id := store.PhotoID(r.PathValue("photoID"))

	var body movePhotoRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request",
			"corps JSON invalide : "+err.Error())
		return
	}
	if body.ToGroup == "" {
		s.writeError(w, http.StatusBadRequest, "bad_request",
			"le champ \"to_group\" est requis")
		return
	}

	err := s.store.MovePhoto(id, store.GroupID(body.ToGroup))
	switch {
	case err == nil:
		s.log.Info("move", "photo", string(id), "to", body.ToGroup)
		s.writeOK(w)
	case errors.Is(err, store.ErrPhotoNotFound), errors.Is(err, store.ErrGroupNotFound):
		s.writeError(w, http.StatusNotFound, "not_found", err.Error())
	default:
		s.writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	}
}
