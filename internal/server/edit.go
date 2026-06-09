package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/sgaunet/moraine/internal/store"
)

type patchGroupRequest struct {
	Label      *string `json:"label"`
	DestSubdir *string `json:"dest_subdir"`
}

// handlePatchGroup edits a group's label and/or destination sub-directory (US4).
// At least one field must be present. An escaping dest_subdir yields 422.
func (s *Server) handlePatchGroup(w http.ResponseWriter, r *http.Request) {
	id := store.GroupID(r.PathValue("groupID"))

	var body patchGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request",
			"corps JSON invalide : "+err.Error())
		return
	}
	if body.Label == nil && body.DestSubdir == nil {
		s.writeError(w, http.StatusBadRequest, "bad_request",
			"fournir au moins un champ : \"label\" ou \"dest_subdir\"")
		return
	}

	if body.Label != nil {
		if err := s.store.SetLabel(id, *body.Label); err != nil {
			s.mapEditError(w, err)
			return
		}
	}
	if body.DestSubdir != nil {
		if err := s.store.SetDestSubdir(id, *body.DestSubdir); err != nil {
			s.mapEditError(w, err)
			return
		}
	}
	s.log.Info("edit", "group", string(id))
	s.writeOK(w)
}

func (s *Server) mapEditError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrGroupNotFound):
		s.writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, store.ErrInvalidDestSubdir):
		s.writeError(w, http.StatusUnprocessableEntity, "invalid_dest_subdir", err.Error())
	case errors.Is(err, store.ErrEmptyLabel):
		s.writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	default:
		s.writeError(w, http.StatusInternalServerError, "internal", err.Error())
	}
}
