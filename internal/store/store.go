// Package store holds moraine's in-memory server state (groups of photos) and
// the mutations on it, including the disk commit. It depends on the domain
// (photo) but never on transport (Constitution Principle III). Concurrency is
// guarded by a single RWMutex; long I/O is performed outside the lock.
package store

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/sgaunet/moraine/internal/photo"
)

// Typed errors mapped to HTTP status codes by the server layer.
var (
	// ErrGroupNotFound is returned when a GroupID is unknown.
	ErrGroupNotFound = errors.New("groupe inconnu")
	// ErrPhotoNotFound is returned when a PhotoID is unknown.
	ErrPhotoNotFound = errors.New("photo inconnue")
	// ErrInvalidDestSubdir is returned when a destination would escape destRoot.
	ErrInvalidDestSubdir = errors.New("sous-dossier de destination invalide")
	// ErrEmptyLabel is returned when a group label would become empty (VR-5).
	ErrEmptyLabel = errors.New("le libellé ne peut pas être vide")
)

// GroupID and PhotoID are opaque, session-stable identifiers (e.g. "g3", "p128").
type (
	GroupID string
	PhotoID string
)

// PhotoRef is a photo as tracked inside a group (stable until committed, I2).
type PhotoRef struct {
	ID     PhotoID
	Path   string
	Name   string
	Taken  time.Time
	Format photo.Format
}

// Group is an event: a non-empty set of photos with an editable label and
// destination sub-directory.
type Group struct {
	ID         GroupID
	Label      string
	DestSubdir string
	Start      time.Time
	End        time.Time
	Photos     []*PhotoRef
	// Manual marks a user-created drop target (optional POST /api/groups). Such
	// a group is serialised even while empty so it can receive dragged photos;
	// pipeline-built groups always have ≥1 photo (I3).
	Manual bool
}

// FailedPhoto reports a photo that could not be moved during a partial commit.
type FailedPhoto struct {
	ID    PhotoID
	Name  string
	Error string
}

// Store is the concurrency-safe in-memory state.
type Store struct {
	mu       sync.RWMutex
	source   string
	destRoot string
	groups   map[GroupID]*Group
	index    map[PhotoID]GroupID
	seqG     int
	seqP     int
}

// New creates an empty Store bound to a source and destination root (absolute).
func New(source, destRoot string) *Store {
	return &Store{
		source:   source,
		destRoot: destRoot,
		groups:   make(map[GroupID]*Group),
		index:    make(map[PhotoID]GroupID),
	}
}

// Source returns the scanned directory (absolute).
func (s *Store) Source() string { return s.source }

// DestRoot returns the destination root (absolute).
func (s *Store) DestRoot() string { return s.destRoot }

// nextGroupID / nextPhotoID allocate monotonic IDs. Caller holds s.mu.
func (s *Store) nextGroupID() GroupID {
	s.seqG++
	return GroupID(fmt.Sprintf("g%d", s.seqG))
}

func (s *Store) nextPhotoID() PhotoID {
	s.seqP++
	return PhotoID(fmt.Sprintf("p%d", s.seqP))
}

// AddGroup registers a new group from raw photos, allocating stable IDs and
// updating the photo→group index. An empty photo set yields no group (I3) and
// returns nil. Used by BuildFromClusters and by tests.
func (s *Store) AddGroup(label, destSubdir string, start, end time.Time, photos []photo.Photo) *Group {
	if len(photos) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	g := &Group{
		ID:         s.nextGroupID(),
		Label:      label,
		DestSubdir: destSubdir,
		Start:      start,
		End:        end,
		Photos:     make([]*PhotoRef, 0, len(photos)),
	}
	for _, p := range photos {
		ref := &PhotoRef{
			ID:     s.nextPhotoID(),
			Path:   p.Path,
			Name:   p.Name,
			Taken:  p.Taken,
			Format: p.Format,
		}
		g.Photos = append(g.Photos, ref)
		s.index[ref.ID] = g.ID
	}
	s.groups[g.ID] = g
	return g
}

// Photo returns a copy of the PhotoRef with the given ID (for thumb/photo
// handlers), or false if unknown. Read-locked.
func (s *Store) Photo(id PhotoID) (PhotoRef, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ref := s.lookupRefLocked(id)
	if ref == nil {
		return PhotoRef{}, false
	}
	return *ref, true
}

// lookupRefLocked returns the *PhotoRef for id, or nil. Caller holds s.mu (R or W).
func (s *Store) lookupRefLocked(id PhotoID) *PhotoRef {
	gid, ok := s.index[id]
	if !ok {
		return nil
	}
	g, ok := s.groups[gid]
	if !ok {
		return nil
	}
	for _, ref := range g.Photos {
		if ref.ID == id {
			return ref
		}
	}
	return nil
}

// ---- JSON DTOs (server → UI boundary, snake_case, decoupled types) ----------

// PhotoDTO is the serialised form of a PhotoRef.
type PhotoDTO struct {
	ID       PhotoID   `json:"id"`
	Name     string    `json:"name"`
	Taken    time.Time `json:"taken"`
	ThumbURL string    `json:"thumb_url"`
	PhotoURL string    `json:"photo_url"`
}

// GroupDTO is the serialised form of a Group.
type GroupDTO struct {
	ID         GroupID    `json:"id"`
	Label      string     `json:"label"`
	DestSubdir string     `json:"dest_subdir"`
	Start      time.Time  `json:"start"`
	End        time.Time  `json:"end"`
	Count      int        `json:"count"`
	Photos     []PhotoDTO `json:"photos"`
}

// GroupsResponse is the payload of GET /api/groups.
type GroupsResponse struct {
	Groups []GroupDTO `json:"groups"`
}

// Snapshot builds the DTO for all groups, ordered chronologically by Start
// (ties broken by ID for determinism). Empty groups are never serialised (I3).
func (s *Store) Snapshot() GroupsResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()

	groups := make([]*Group, 0, len(s.groups))
	for _, g := range s.groups {
		if len(g.Photos) == 0 && !g.Manual {
			continue // I3: empty pipeline groups are never serialised
		}
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Start.Equal(groups[j].Start) {
			return groups[i].ID < groups[j].ID
		}
		return groups[i].Start.Before(groups[j].Start)
	})

	resp := GroupsResponse{Groups: make([]GroupDTO, 0, len(groups))}
	for _, g := range groups {
		resp.Groups = append(resp.Groups, toGroupDTO(g))
	}
	return resp
}

// toGroupDTO maps a *Group to its serialised form. Caller holds s.mu.
func toGroupDTO(g *Group) GroupDTO {
	dto := GroupDTO{
		ID:         g.ID,
		Label:      g.Label,
		DestSubdir: g.DestSubdir,
		Start:      g.Start,
		End:        g.End,
		Count:      len(g.Photos),
		Photos:     make([]PhotoDTO, 0, len(g.Photos)),
	}
	for _, ref := range g.Photos {
		dto.Photos = append(dto.Photos, PhotoDTO{
			ID:       ref.ID,
			Name:     ref.Name,
			Taken:    ref.Taken,
			ThumbURL: "/thumb/" + string(ref.ID),
			PhotoURL: "/photo/" + string(ref.ID),
		})
	}
	return dto
}
