package organize

import (
	"os"
	"path/filepath"
	"strings"
)

// companionKind classifies how a candidate file relates to a photo's name.
type companionKind int

const (
	notCompanion      companionKind = iota
	companionAppended               // candidate = <photo full name> + suffix (e.g. IMG.jpg → IMG.jpg.xmp)
	companionBaseName               // candidate shares the photo's base name, different ext (IMG.jpg → IMG.xmp)
)

// matchCompanion classifies candidate (a file name found in the same directory as
// the photo) against the photo's file name. It returns the appended suffix (only
// meaningful for companionAppended) and the kind. See
// specs/006-sidecar-files/contracts/companion-matching.md for the full rules.
func matchCompanion(photoName, candidate string) (suffix string, kind companionKind) {
	if candidate == photoName {
		return "", notCompanion // the photo itself is never its own companion
	}
	// Rule (a) — appended: candidate begins with the photo's full name plus more.
	if strings.HasPrefix(candidate, photoName) {
		return candidate[len(photoName):], companionAppended
	}
	// Rule (b) — base name: same stem, different non-empty extension.
	cext := filepath.Ext(candidate)
	if cext != "" && strings.TrimSuffix(candidate, cext) == stem(photoName) {
		return "", companionBaseName
	}
	return "", notCompanion
}

// companionTargetName derives the destination name for a companion so it stays
// linked to the photo's final placed name (finalPhotoName), which may differ from
// the source name after a ` (N)` collision rename.
func companionTargetName(finalPhotoName, candidate, suffix string, kind companionKind) string {
	switch kind {
	case companionAppended:
		return finalPhotoName + suffix
	case companionBaseName:
		return stem(finalPhotoName) + filepath.Ext(candidate)
	case notCompanion:
		return candidate
	}
	return candidate
}

// stem returns a file name with its final extension removed.
func stem(name string) string {
	return strings.TrimSuffix(name, filepath.Ext(name))
}

// placeCompanions discovers the companion (sidecar) files of the photo at
// photoSrc and copies each into dir, naming it to track the photo's final placed
// name (finalPhotoName). It reuses placeOne, so companions inherit the copy-only,
// no-overwrite, skip-identical and ` (N)`-suffix guarantees. A per-companion
// failure is recorded in its Result.Err (non-fatal). Files that are themselves
// scanned primaries (FR-006) or that live under the destination tree (FR-007) are
// skipped; only regular files are considered.
func (o *Organizer) placeCompanions(dir, photoSrc, finalPhotoName string) []Result {
	srcDir := filepath.Dir(photoSrc)
	photoName := filepath.Base(photoSrc)
	cleanDest := filepath.Clean(o.DestRoot)

	var results []Result
	for _, e := range o.readDir(srcDir) {
		if !e.Type().IsRegular() {
			continue // skip directories, symlinks, special files
		}
		name := e.Name()
		suffix, kind := matchCompanion(photoName, name)
		if kind == notCompanion {
			continue
		}
		candidate := filepath.Join(srcDir, name)
		if o.IsPrimary != nil && o.IsPrimary(candidate) {
			continue // a file that is itself sorted as a photo (FR-006)
		}
		if withinDest(candidate, cleanDest) {
			continue // never ingest from the destination tree (FR-007)
		}
		target := companionTargetName(finalPhotoName, name, suffix, kind)
		res := Result{Source: candidate, IsCompanion: true, Of: photoSrc}
		res.Dest, res.Action, res.Err = o.placeOne(dir, candidate, target)
		results = append(results, res)
	}
	return results
}

// readDir returns dir's entries, caching one listing per directory so companion
// discovery is linear in the number of source entries (one ReadDir per directory).
// An unreadable directory caches an empty listing (no companions, never fatal).
func (o *Organizer) readDir(dir string) []os.DirEntry {
	if o.dirEntries == nil {
		o.dirEntries = make(map[string][]os.DirEntry)
	}
	if entries, ok := o.dirEntries[dir]; ok {
		return entries
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		entries = nil
	}
	o.dirEntries[dir] = entries
	return entries
}

// withinDest reports whether path lies within (or is) the cleaned destination root.
func withinDest(path, cleanDest string) bool {
	cp := filepath.Clean(path)
	if cp == cleanDest {
		return true
	}
	rel, err := filepath.Rel(cleanDest, cp)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
