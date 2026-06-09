package store

import (
	"strings"
	"time"
)

// CreateGroup creates an empty, user-targetable group (optional endpoint
// POST /api/groups). It is flagged Manual so it remains visible while empty.
// A blank label is replaced by a sensible default.
func (s *Store) CreateGroup(label string) GroupDTO {
	label = strings.TrimSpace(label)
	if label == "" {
		label = "nouveau groupe"
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	g := &Group{
		ID:         s.nextGroupID(),
		Label:      label,
		DestSubdir: label,
		Start:      now,
		End:        now,
		Manual:     true,
	}
	s.groups[g.ID] = g
	return toGroupDTO(g)
}
