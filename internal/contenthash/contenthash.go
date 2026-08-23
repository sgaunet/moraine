// Package contenthash defines moraine's content identity: two files are the same
// when their bytes are the same. It is the single source of truth for "same
// content" across the organizer (dedup on copy) and the clean subcommand (matching
// originals to their copies), and offers the two shapes those callers need:
//
//   - Hash streams a file into a SHA-256 digest, so many files can be indexed and
//     looked up by content (clean builds a size → digest-set index).
//   - Equal compares two files byte by byte, short-circuiting at the first
//     difference. It is the cheaper and exact answer when the question is about one
//     specific pair, which is all the organizer ever asks.
//
// Pure Go, no transport or global state (Constitution Principle III).
package contenthash

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
)

// Sum is the SHA-256 content digest of a file. It is comparable, so it can be used
// directly as a map key.
type Sum [sha256.Size]byte

// chunkSize is the read granularity of Equal: large enough that the syscall cost
// disappears next to the copy itself, small enough to stay off the heap's radar.
const chunkSize = 64 * 1024

// Hash returns the SHA-256 content digest of the file at path. The file is streamed
// through the hash (constant memory regardless of size); it is never buffered whole.
// Open and read failures are wrapped with context.
func Hash(path string) (Sum, error) {
	var sum Sum
	f, err := os.Open(path)
	if err != nil {
		return sum, fmt.Errorf("opening %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return sum, fmt.Errorf("reading %q: %w", path, err)
	}
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

// Equal reports whether the files at a and b hold exactly the same bytes. It streams
// both in lockstep and returns false as soon as they diverge — including on a length
// mismatch — so it never reads more than it must and never digests anything, leaving
// no room for a digest collision to be mistaken for identity. Open and read failures
// are wrapped with context.
func Equal(a, b string) (bool, error) {
	fa, err := os.Open(a)
	if err != nil {
		return false, fmt.Errorf("opening %q: %w", a, err)
	}
	defer func() { _ = fa.Close() }()

	fb, err := os.Open(b)
	if err != nil {
		return false, fmt.Errorf("opening %q: %w", b, err)
	}
	defer func() { _ = fb.Close() }()

	ba := make([]byte, chunkSize)
	bb := make([]byte, chunkSize)
	for {
		// A genuine read failure is checked before the comparison: it must surface as
		// an error, never be mistaken for "different content" (which would make the
		// organizer suffix-rename a photo it merely failed to read).
		na, ea := io.ReadFull(fa, ba)
		if ea != nil && !isEOF(ea) {
			return false, fmt.Errorf("reading %q: %w", a, ea)
		}
		nb, eb := io.ReadFull(fb, bb)
		if eb != nil && !isEOF(eb) {
			return false, fmt.Errorf("reading %q: %w", b, eb)
		}
		if na != nb || !bytes.Equal(ba[:na], bb[:nb]) {
			return false, nil // diverged, or one file ended early
		}
		if ea != nil || eb != nil {
			// io.ReadFull signals the end as io.EOF (nothing read) or
			// io.ErrUnexpectedEOF (a short final chunk). A short read on one side only
			// would have differed in length above, so both files ended here, on the
			// same byte count, with the same bytes.
			return true, nil
		}
	}
}

// isEOF reports whether err is one of io.ReadFull's two end-of-file signals.
func isEOF(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}
