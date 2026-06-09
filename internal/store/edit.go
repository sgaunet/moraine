package store

import "strings"

// SetLabel updates a group's label. An empty/whitespace label is rejected so
// the VR-5 "never empty" invariant holds. Unknown group → ErrGroupNotFound.
func (s *Store) SetLabel(id GroupID, label string) error {
	label = strings.TrimSpace(label)
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[id]
	if !ok {
		return ErrGroupNotFound
	}
	if label == "" {
		return ErrEmptyLabel
	}
	g.Label = label
	return nil
}

// SetDestSubdir updates a group's destination sub-directory after validating
// it stays under destRoot (VR-1). Unknown group → ErrGroupNotFound; an escaping
// path → ErrInvalidDestSubdir.
func (s *Store) SetDestSubdir(id GroupID, subdir string) error {
	subdir = strings.TrimSpace(subdir)
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[id]
	if !ok {
		return ErrGroupNotFound
	}
	if _, err := safeJoin(s.destRoot, subdir); err != nil {
		return err // wraps ErrInvalidDestSubdir
	}
	g.DestSubdir = subdir
	return nil
}
