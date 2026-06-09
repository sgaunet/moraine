package store

// MovePhoto relocates a photo from its current group to toGroup. This is a
// purely logical move (no disk effect, FR-007). A source group left empty is
// removed (I3). Unknown photo or target group yield typed errors.
func (s *Store) MovePhoto(pid PhotoID, toGroup GroupID) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	fromGID, ok := s.index[pid]
	if !ok {
		return ErrPhotoNotFound
	}
	target, ok := s.groups[toGroup]
	if !ok {
		return ErrGroupNotFound
	}
	if fromGID == toGroup {
		return nil // already there; no-op
	}

	from := s.groups[fromGID]
	var ref *PhotoRef
	kept := make([]*PhotoRef, 0, len(from.Photos))
	for _, r := range from.Photos {
		if r.ID == pid {
			ref = r
		} else {
			kept = append(kept, r)
		}
	}
	if ref == nil {
		// Index/group desync should never happen; fail loudly rather than silently.
		return ErrPhotoNotFound
	}

	from.Photos = kept
	target.Photos = append(target.Photos, ref)
	s.index[pid] = toGroup

	if len(from.Photos) == 0 {
		delete(s.groups, fromGID) // emptied source group disappears (I3)
	} else {
		recomputeBounds(from)
	}
	recomputeBounds(target)
	return nil
}

// recomputeBounds refreshes a group's Start/End from its photos. Caller holds
// the write lock. Groups always have at least one photo here.
func recomputeBounds(g *Group) {
	if len(g.Photos) == 0 {
		return
	}
	start := g.Photos[0].Taken
	end := g.Photos[0].Taken
	for _, r := range g.Photos[1:] {
		if r.Taken.Before(start) {
			start = r.Taken
		}
		if r.Taken.After(end) {
			end = r.Taken
		}
	}
	g.Start = start
	g.End = end
}
