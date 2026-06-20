package organize

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
)

// copyFile copies src to dst durably and non-destructively: it opens dst with
// O_EXCL (so it can never overwrite an existing file), streams the bytes,
// fsyncs, and closes. The source file is never modified or removed (copy-only).
func copyFile(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("ouverture source: %w", err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("création destination: %w", err)
	}

	if _, err = io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst) // clean up partial copy
		return fmt.Errorf("copie: %w", err)
	}
	if err = out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return fmt.Errorf("fsync: %w", err)
	}
	if err = out.Close(); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("fermeture destination: %w", err)
	}
	return nil
}

// sameContent reports whether files a and b have identical content. It
// short-circuits on a size mismatch before comparing SHA-256 digests, so
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
	ah, err := hashFile(a)
	if err != nil {
		return false, err
	}
	bh, err := hashFile(b)
	if err != nil {
		return false, err
	}
	return ah == bh, nil
}

// hashFile returns the SHA-256 digest of the file at path as an array.
func hashFile(path string) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	f, err := os.Open(path)
	if err != nil {
		return sum, fmt.Errorf("ouverture %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return sum, fmt.Errorf("lecture %q: %w", path, err)
	}
	copy(sum[:], h.Sum(nil))
	return sum, nil
}
