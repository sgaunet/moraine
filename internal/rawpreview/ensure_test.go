package rawpreview_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sgaunet/moraine/internal/exiftooltest"
	"github.com/sgaunet/moraine/internal/rawpreview"
)

func TestEnsureAvailableOK(t *testing.T) {
	path, err := exiftooltest.Stub(t.TempDir(), exiftooltest.Options{Version: "13.55"})
	if err != nil {
		t.Fatal(err)
	}
	if err := rawpreview.EnsureAvailable(path); err != nil {
		t.Fatalf("EnsureAvailable: %v; want nil for a working exiftool", err)
	}
}

func TestEnsureAvailableMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope-exiftool")
	err := rawpreview.EnsureAvailable(missing)
	if err == nil {
		t.Fatal("expected an error for a missing exiftool")
	}
	for _, want := range []string{"exiftool", "-exiftool", "install"} {
		if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(want)) {
			t.Errorf("error message missing %q: %v", want, err)
		}
	}
}

func TestEnsureAvailableNotRunnable(t *testing.T) {
	path, err := exiftooltest.Stub(t.TempDir(), exiftooltest.Options{VerFails: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := rawpreview.EnsureAvailable(path); err == nil {
		t.Fatal("expected an error when `exiftool -ver` fails")
	}
}

func TestEnsureAvailableViaPATH(t *testing.T) {
	dir := t.TempDir()
	if _, err := exiftooltest.Stub(dir, exiftooltest.Options{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir) // resolve "exiftool" (no separator) to the stub via LookPath
	if err := rawpreview.EnsureAvailable("exiftool"); err != nil {
		t.Fatalf("EnsureAvailable(\"exiftool\") via PATH: %v", err)
	}
}
