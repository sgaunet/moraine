package contenthash_test

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sgaunet/moraine/internal/contenthash"
)

func TestHashIdentityAndDifference(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.bin")
	b := filepath.Join(dir, "b.bin")
	c := filepath.Join(dir, "c.bin")
	for path, data := range map[string]string{a: "hello", b: "hello", c: "world"} {
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ha, err := contenthash.Hash(a)
	if err != nil {
		t.Fatalf("hash a: %v", err)
	}
	hb, err := contenthash.Hash(b)
	if err != nil {
		t.Fatalf("hash b: %v", err)
	}
	hc, err := contenthash.Hash(c)
	if err != nil {
		t.Fatalf("hash c: %v", err)
	}

	if ha != hb {
		t.Error("identical content must hash equal")
	}
	if ha == hc {
		t.Error("different content must hash differently")
	}

	// Known SHA-256 of "hello".
	const wantHex = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if got := hex.EncodeToString(ha[:]); got != wantHex {
		t.Errorf("hash(hello) = %s, want %s", got, wantHex)
	}
}

func TestHashMissingFile(t *testing.T) {
	if _, err := contenthash.Hash(filepath.Join(t.TempDir(), "nope.bin")); err == nil {
		t.Fatal("expected an error hashing a missing file")
	}
}

// TestEqual covers the pairwise byte comparison, including the boundaries where a
// chunked reader is easiest to get wrong: an empty pair, a difference in the very
// first and very last byte, and one file being a prefix of the other.
func TestEqual(t *testing.T) {
	const chunk = 64 * 1024
	long := strings.Repeat("photo", chunk/5) // spans several read chunks

	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"identical short", "hello", "hello", true},
		{"both empty", "", "", true},
		{"identical multi-chunk", long, long, true},
		{"differs in first byte", "hello", "jello", false},
		{"differs in middle byte", "hello", "heXlo", false},
		{"differs in last byte", "hello", "hellp", false},
		{"differs past the first chunk", long + "a", long + "b", false},
		{"prefix of the other", "hell", "hello", false},
		{"empty against non-empty", "", "x", false},
		{"same length, all different", "abcd", "wxyz", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			a := filepath.Join(dir, "a.bin")
			b := filepath.Join(dir, "b.bin")
			if err := os.WriteFile(a, []byte(tc.a), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(b, []byte(tc.b), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := contenthash.Equal(a, b)
			if err != nil {
				t.Fatalf("Equal: %v", err)
			}
			if got != tc.want {
				t.Errorf("Equal = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestEqualAgreesWithHash pins the two halves of the package to one another: what
// Equal calls the same content must hash the same, and vice versa. organize compares
// pairs while clean matches by digest, so a disagreement here would mean a photo
// copied by one and not recognised by the other.
func TestEqualAgreesWithHash(t *testing.T) {
	dir := t.TempDir()
	paths := map[string]string{"a.bin": "same", "b.bin": "same", "c.bin": "diff"}
	for name, data := range paths {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, pair := range [][2]string{{"a.bin", "b.bin"}, {"a.bin", "c.bin"}} {
		a := filepath.Join(dir, pair[0])
		b := filepath.Join(dir, pair[1])
		equal, err := contenthash.Equal(a, b)
		if err != nil {
			t.Fatalf("Equal(%s, %s): %v", pair[0], pair[1], err)
		}
		ha, err := contenthash.Hash(a)
		if err != nil {
			t.Fatal(err)
		}
		hb, err := contenthash.Hash(b)
		if err != nil {
			t.Fatal(err)
		}
		if equal != (ha == hb) {
			t.Errorf("%s vs %s: Equal = %v but digests equal = %v", pair[0], pair[1], equal, ha == hb)
		}
	}
}

func TestEqualMissingFile(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "present.bin")
	if err := os.WriteFile(present, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "nope.bin")

	if _, err := contenthash.Equal(missing, present); err == nil {
		t.Error("expected an error when the first file is missing")
	}
	if _, err := contenthash.Equal(present, missing); err == nil {
		t.Error("expected an error when the second file is missing")
	}
}
