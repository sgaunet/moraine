// Package diskspace answers one question: how many bytes are free on the filesystem
// that holds a given path. It exists so a run can say "the destination looks too
// small" once, up front, instead of reporting the same full disk once per photo.
//
// The syscall behind it is not portable, so it lives in a build-tagged file per
// platform; this one holds the exported API and the only piece of real logic — the
// walk up to an ancestor that exists.
//
// Pure Go, no transport or global state (Constitution Principle III).
package diskspace

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
)

// Available reports the number of bytes free on the filesystem that holds path.
//
// path need not exist. moraine's destination root is created on first use, so on a
// first run there is nothing there to interrogate — and asking the nearest existing
// ancestor is not a workaround but the right question: that is the filesystem which
// will host the destination once it is created. The walk terminates at the root,
// which always exists.
//
// Only a missing path is walked past: syscall.Errno maps ENOENT — and nothing else —
// onto fs.ErrNotExist, so a path that cannot host a directory at all (named under a
// regular file, say, which fails ENOTDIR) is reported as the error it is rather than
// silently answered for some ancestor. Callers treat that as "free space unknown";
// the path itself is organize's to reject.
func Available(path string) (uint64, error) {
	p := filepath.Clean(path)
	for {
		avail, err := statfsAvail(p)
		if err == nil {
			return avail, nil
		}
		// Anything other than "not there yet" is a real failure, and so is running out
		// of ancestors to try: report it against the path the caller asked about.
		parent := filepath.Dir(p)
		if !errors.Is(err, fs.ErrNotExist) || parent == p {
			return 0, fmt.Errorf("determining free space for %q: %w", path, err)
		}
		p = parent
	}
}
