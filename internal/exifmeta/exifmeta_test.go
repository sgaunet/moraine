package exifmeta_test

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/exifmeta"
	"github.com/sgaunet/moraine/internal/photo"
)

// writeJPEG creates a valid (EXIF-less) JPEG and back-dates its mtime.
func writeJPEG(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestReadFallsBackToMtimeWhenNoEXIF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "noexif.jpg")
	want := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)
	writeJPEG(t, path, want)

	p, err := exifmeta.Read(path, photo.JPEG)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if p.Name != "noexif.jpg" {
		t.Errorf("Name = %q", p.Name)
	}
	if p.Format != photo.JPEG {
		t.Errorf("Format = %v; want JPEG", p.Format)
	}
	// No EXIF date → must use mtime (within filesystem resolution).
	if diff := p.Taken.Sub(want); diff > time.Second || diff < -time.Second {
		t.Errorf("Taken = %v; want ~mtime %v", p.Taken, want)
	}
	if p.GPS != nil {
		t.Errorf("GPS = %v; want nil when absent", p.GPS)
	}
	if p.Altitude != nil {
		t.Errorf("Altitude = %v; want nil when absent", p.Altitude)
	}
}

func TestReadUnreadableDataStillFallsBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "garbage.jpg")
	want := time.Date(2019, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.WriteFile(path, []byte("not a real jpeg"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, want, want); err != nil {
		t.Fatal(err)
	}

	p, err := exifmeta.Read(path, photo.JPEG)
	if err != nil {
		t.Fatalf("Read should not fail on undecodable EXIF: %v", err)
	}
	if diff := p.Taken.Sub(want); diff > time.Second || diff < -time.Second {
		t.Errorf("Taken = %v; want ~mtime %v", p.Taken, want)
	}
}

func TestReadMissingFileErrors(t *testing.T) {
	_, err := exifmeta.Read(filepath.Join(t.TempDir(), "nope.jpg"), photo.JPEG)
	if err == nil {
		t.Fatal("expected error for a missing file")
	}
}
