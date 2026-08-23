package organize

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/sgaunet/moraine/internal/contenthash"
)

// tmpPattern names the in-progress copies. The leading dot keeps them out of the
// way of every name moraine cares about: they can never collide with a photo name
// and are invisible to existingIdentical's " (N)" variant scan.
const tmpPattern = ".moraine-*.tmp"

// copyFile copies src to dst durably, non-destructively and atomically: the bytes
// go to a temporary file in dst's own directory, are fsynced, get src's modification
// time, and only then appear at dst under a single link(2) — which fails if dst
// exists, so a copy can never overwrite. The source is never modified or removed.
//
// The atomicity is what keeps a crash from costing a name: because dst is only ever
// created by the final link, a SIGKILL or power loss can leave a stray
// ".moraine-*.tmp" behind but never a truncated file at the canonical name. (A
// truncated file there would be indistinguishable from different content, so every
// later run would suffix-rename the real photo and let the stub keep the good name.)
// Sweeping such leftovers is out of scope: they are hidden, harmless, and cheap to
// delete by hand.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening source: %w", err)
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}

	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, tmpPattern)
	if err != nil {
		return fmt.Errorf("creating temporary file: %w", err)
	}
	// Until the link succeeds the temporary file is the only thing written, so every
	// failure path below leaves the destination directory exactly as it was.
	defer func() { _ = os.Remove(tmp.Name()) }()

	if err := writeTemp(tmp, in, info.ModTime()); err != nil {
		return err
	}
	if err := link(tmp.Name(), dst); err != nil {
		return err
	}
	return syncDir(dir)
}

// writeTemp streams in into the already-created temporary file, makes its bytes
// durable, and stamps it with the source's modification time. It always closes tmp.
//
// Preserving mtime is not cosmetic: exifmeta falls back to the file's modification
// time when a photo has no readable EXIF date, so a copy stamped "now" would destroy
// the only date signal such a photo has.
func writeTemp(tmp *os.File, in io.Reader, modTime time.Time) error {
	name := tmp.Name()
	// os.CreateTemp opens 0o600; destinations are readable like the rest of the tree.
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("setting destination mode: %w", err)
	}
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("copying: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing destination: %w", err)
	}
	// A zero atime leaves the access time alone; only the modification time matters.
	if err := os.Chtimes(name, time.Time{}, modTime); err != nil {
		return fmt.Errorf("preserving modification time: %w", err)
	}
	return nil
}

// link publishes the finished temporary file at dst. link(2) fails with EEXIST
// rather than clobbering, which makes "never overwrite" a property of the syscall
// instead of a check the caller has to remember.
//
// Filesystems without hard links (FAT/exFAT/SMB — ordinary destinations for a photo
// library on an external drive) reject it outright; there the copy falls back to a
// rename guarded by an existence check. rename(2) does overwrite, so that path is a
// check-then-act rather than an atomic guarantee — acceptable because moraine places
// files from a single sequential loop, and it still never leaves a partial file at
// the canonical name.
func link(tmpName, dst string) error {
	err := os.Link(tmpName, dst)
	if err == nil {
		return nil
	}
	if !linkUnsupported(err) {
		return fmt.Errorf("creating destination: %w", err)
	}
	if exists(dst) {
		return fmt.Errorf("creating destination %q: %w", dst, fs.ErrExist)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return fmt.Errorf("creating destination: %w", err)
	}
	return nil
}

// linkUnsupported reports whether err means "this filesystem has no hard links"
// rather than "this link could not be made".
func linkUnsupported(err error) bool {
	return errors.Is(err, errors.ErrUnsupported) ||
		errors.Is(err, fs.ErrPermission) ||
		errors.Is(err, fs.ErrInvalid)
}

// syncDir fsyncs a directory so the entries created in it survive a crash. Syncing
// the file's data is not enough on its own: without this the directory entry can
// still be lost, taking the freshly copied photo with it.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("opening directory %q: %w", dir, err)
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("fsync directory %q: %w", dir, err)
	}
	return nil
}

// sameContent reports whether files a and b have identical content. It
// short-circuits on a size mismatch before comparing the bytes themselves, so
// re-runs can cheaply skip already-copied photos.
func sameContent(a, b string) (bool, error) {
	ai, err := os.Stat(a)
	if err != nil {
		return false, fmt.Errorf("stat %q: %w", a, err)
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false, fmt.Errorf("stat %q: %w", b, err)
	}
	if ai.Size() != bi.Size() {
		return false, nil
	}
	return contenthash.Equal(a, b)
}
