package server

import (
	"errors"
	"net/http"

	"github.com/sgaunet/moraine/internal/store"
)

type commitResultDTO struct {
	Moved int    `json:"moved"`
	Dest  string `json:"dest"`
}

type failedPhotoDTO struct {
	Photo string `json:"photo"`
	Error string `json:"error"`
}

type partialCommitDTO struct {
	Moved  int              `json:"moved"`
	Failed []failedPhotoDTO `json:"failed"`
}

// handleCommit moves a group's files to disk (US2). The UI must have obtained
// explicit user confirmation before calling this (FR-022). Maps to 200 (total
// success), 207 (partial), 404 (unknown group) or 422 (invalid destination).
func (s *Server) handleCommit(w http.ResponseWriter, r *http.Request) {
	id := store.GroupID(r.PathValue("groupID"))

	res, err := s.store.Commit(id)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrGroupNotFound):
			s.writeError(w, http.StatusNotFound, "not_found", err.Error())
		case errors.Is(err, store.ErrInvalidDestSubdir):
			s.writeError(w, http.StatusUnprocessableEntity, "invalid_dest_subdir",
				err.Error()+" — corrigez le sous-dossier de destination puis réessayez")
		default:
			s.writeError(w, http.StatusInternalServerError, "commit_failed", err.Error())
		}
		return
	}

	if len(res.Failed) == 0 {
		s.log.Info("commit", "group", string(id), "dest", res.Dest, "moved", res.Moved)
		s.writeJSON(w, http.StatusOK, commitResultDTO{Moved: res.Moved, Dest: res.Dest})
		return
	}

	// Partial success (I5): report what failed, keep those photos in the group.
	failed := make([]failedPhotoDTO, 0, len(res.Failed))
	for _, f := range res.Failed {
		failed = append(failed, failedPhotoDTO{Photo: string(f.ID), Error: f.Error})
	}
	s.log.Warn("commit partial", "group", string(id), "moved", res.Moved, "failed", len(failed))
	s.writeJSON(w, http.StatusMultiStatus, partialCommitDTO{Moved: res.Moved, Failed: failed})
}
