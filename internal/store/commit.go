package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// CommitResult reports the outcome of moving a group's files to disk.
// Failed is empty on total success (HTTP 200); non-empty on partial success
// (HTTP 207).
type CommitResult struct {
	Moved  int
	Dest   string
	Failed []FailedPhoto
}

// Commit moves every file of the group to destRoot/<dest_subdir>. The group is
// removed from the state only if all files moved (I1); otherwise it keeps only
// the photos that failed (I5). Long disk I/O happens outside the lock (R8):
// a short RLock snapshots the group, the moves run lock-free, and a short Lock
// applies the final mutation.
func (s *Store) Commit(id GroupID) (CommitResult, error) {
	// 1. Snapshot under RLock.
	s.mu.RLock()
	g, ok := s.groups[id]
	if !ok {
		s.mu.RUnlock()
		return CommitResult{}, ErrGroupNotFound
	}
	destSubdir := g.DestSubdir
	refs := make([]*PhotoRef, len(g.Photos))
	copy(refs, g.Photos)
	s.mu.RUnlock()

	// 2. Resolve & validate destination (VR-1), create it.
	destDir, err := safeJoin(s.destRoot, destSubdir)
	if err != nil {
		return CommitResult{}, err // wraps ErrInvalidDestSubdir
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return CommitResult{}, fmt.Errorf("création du dossier de destination %q: %w", destDir, err)
	}

	// 3. Move files (lock-free), tracking successes and failures.
	moved := make(map[PhotoID]bool, len(refs))
	var failed []FailedPhoto
	for _, ref := range refs {
		name := uniqueName(destDir, ref.Name) // VR-3 / I4: never overwrite
		dst := filepath.Join(destDir, name)
		if err := moveFile(ref.Path, dst); err != nil {
			failed = append(failed, FailedPhoto{
				ID:    ref.ID,
				Name:  ref.Name,
				Error: humanError(err),
			})
			continue
		}
		moved[ref.ID] = true
	}

	// 4. Final mutation under Lock: drop moved photos; remove group if emptied.
	s.mu.Lock()
	if g, ok := s.groups[id]; ok {
		kept := make([]*PhotoRef, 0, len(g.Photos))
		for _, ref := range g.Photos {
			if moved[ref.ID] {
				delete(s.index, ref.ID)
			} else {
				kept = append(kept, ref)
			}
		}
		if len(kept) == 0 {
			delete(s.groups, id) // total success → group gone (I1)
		} else {
			g.Photos = kept // partial → keep only the failures (I5)
			recomputeBounds(g)
		}
	}
	s.mu.Unlock()

	return CommitResult{Moved: len(moved), Dest: destDir, Failed: failed}, nil
}

// humanError turns a move error into an actionable, user-facing message (VI).
func humanError(err error) string {
	switch {
	case errors.Is(err, os.ErrPermission):
		return "permission refusée"
	case errors.Is(err, os.ErrNotExist):
		return "fichier source introuvable"
	case errors.Is(err, os.ErrExist):
		return "un fichier du même nom existe déjà en destination"
	default:
		return err.Error()
	}
}
