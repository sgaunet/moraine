package organize

// Test-only re-exports of unexported helpers so black-box tests
// (package organize_test) can exercise them.
var (
	SafeJoin    = safeJoin
	CopyFile    = copyFile
	SameContent = sameContent
	WithinDest  = withinDest
)

// UniqueName wraps uniqueName with the real run's notion of a taken name (a file on
// disk), which is what the collision-naming tests exercise.
func UniqueName(dir, name string) string {
	return uniqueName(dir, name, exists)
}

// MatchCompanion wraps matchCompanion for black-box tests, mapping the unexported
// companionKind to a stable string ("appended" | "base" | "none").
func MatchCompanion(photoName, candidate string) (suffix, kind string) {
	s, k := matchCompanion(photoName, candidate)
	return s, kindString(k)
}

// CompanionTargetName wraps companionTargetName for black-box tests; kind is the
// string form returned by MatchCompanion ("appended" | "base").
func CompanionTargetName(finalPhotoName, candidate, suffix, kind string) string {
	return companionTargetName(finalPhotoName, candidate, suffix, kindFromString(kind))
}

func kindString(k companionKind) string {
	switch k {
	case companionAppended:
		return "appended"
	case companionBaseName:
		return "base"
	case notCompanion:
		return "none"
	}
	return "none"
}

func kindFromString(s string) companionKind {
	switch s {
	case "appended":
		return companionAppended
	case "base":
		return companionBaseName
	default:
		return notCompanion
	}
}

// SetAfterPublish installs a hook that runs between publishing a copy and verifying
// it, so a black-box test can corrupt the destination inside that window and prove
// --move refuses to remove the source.
func SetAfterPublish(o *Organizer, fn func(dst string)) {
	o.afterPublish = fn
}
