// Package contenthash defines moraine's content identity: the SHA-256 digest of a
// file's bytes. It is the single source of truth for "same content" across the
// organizer (dedup on copy) and the clean subcommand (matching originals to their
// copies). Pure Go, no transport or global state (Constitution Principle III).
package contenthash

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
)

// Sum is the SHA-256 content digest of a file. It is comparable, so it can be used
// directly as a map key.
type Sum [sha256.Size]byte

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
