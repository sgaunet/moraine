package server

import (
	"encoding/json"
	"net/http"
)

type createGroupRequest struct {
	Label string `json:"label"`
}

// handleCreateGroup creates an empty, drag-targetable group (optional endpoint).
// The request body is optional.
func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	var body createGroupRequest
	// Body is optional; ignore decode errors on an empty/absent body.
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	dto := s.store.CreateGroup(body.Label)
	s.log.Info("create group", "group", string(dto.ID), "label", dto.Label)
	s.writeJSON(w, http.StatusCreated, dto)
}
