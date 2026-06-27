package contenthash_test

import (
	"encoding/hex"
	"os"
	"path/filepath"
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
