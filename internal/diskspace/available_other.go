//go:build !unix

package diskspace

import "errors"

// statfsAvail has no portable implementation outside unix. Releases target linux and
// darwin only, but the tree must still build for a contributor on another platform —
// and the caller treats a failure here as "free space unknown", not as a run failure,
// so this degrades to one debug line rather than to a broken build.
func statfsAvail(_ string) (uint64, error) {
	return 0, errors.ErrUnsupported
}
