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

// wallClock is the layout used to compare two times by their wall-clock reading
// alone, ignoring the zone they carry.
const wallClock = "2006-01-02 15:04:05"

// mtimeWallClock returns the wall-clock reading the filesystem reports for path.
// os.Chtimes stores an instant; what a photo organizer cares about — and what
// exifmeta returns, stamped UTC — is the wall clock that instant reads as.
func mtimeWallClock(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.ModTime().Format(wallClock)
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
	// No EXIF date → must use the mtime's wall clock, in the UTC-naive frame.
	if got, w := p.Taken.Format(wallClock), mtimeWallClock(t, path); got != w {
		t.Errorf("Taken wall clock = %s; want mtime wall clock %s", got, w)
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
	if got, w := p.Taken.Format(wallClock), mtimeWallClock(t, path); got != w {
		t.Errorf("Taken wall clock = %s; want mtime wall clock %s", got, w)
	}
}

// TestReadTakenIsUTCNaiveWallClock pins the package's frame invariant: whatever
// the source of the date, Taken carries no zone offset. cluster subtracts these
// values from each other, so a single frame is what keeps the gap arithmetic
// meaningful (mixing a UTC-naive EXIF date with a zoned mtime skews it by the
// local offset).
func TestReadTakenIsUTCNaiveWallClock(t *testing.T) {
	dir := t.TempDir()
	mtime := time.Date(2022, 7, 8, 9, 10, 11, 0, time.UTC)

	valid := filepath.Join(dir, "valid.jpg")
	writeJPEG(t, valid, mtime)
	garbage := filepath.Join(dir, "garbage.jpg")
	if err := os.WriteFile(garbage, []byte("not a real jpeg"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(garbage, mtime, mtime); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{valid, garbage} {
		p, err := exifmeta.Read(path, photo.JPEG)
		if err != nil {
			t.Fatalf("Read(%s): %v", filepath.Base(path), err)
		}
		if p.Taken.Location() != time.UTC {
			t.Errorf("%s: Taken location = %v; want UTC", filepath.Base(path), p.Taken.Location())
		}
		// The wall clock must match the reading the filesystem reports, so the
		// destination folder stays the day the photo reads as locally.
		if got, w := p.Taken.Format(wallClock), mtimeWallClock(t, path); got != w {
			t.Errorf("%s: Taken wall clock = %s; want %s", filepath.Base(path), got, w)
		}
	}
}

func TestReadMissingFileErrors(t *testing.T) {
	_, err := exifmeta.Read(filepath.Join(t.TempDir(), "nope.jpg"), photo.JPEG)
	if err == nil {
		t.Fatal("expected error for a missing file")
	}
}

// TestReadPrefersFilenameDateOverMtime pins the middle dating tier. A batch of
// downloads shares one mtime, so dating them by mtime collapses them into a single
// bogus event; their names still carry the day each was taken.
func TestReadPrefersFilenameDateOverMtime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "IMG-20230815-WA0001.jpg")
	// A download time months after the capture date in the name.
	writeJPEG(t, path, time.Date(2026, 1, 1, 9, 30, 0, 0, time.UTC))

	p, err := exifmeta.Read(path, photo.JPEG)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got, want := p.Taken.Format(wallClock), "2023-08-15 00:00:00"; got != want {
		t.Errorf("Taken = %s; want the date in the file name, %s", got, want)
	}
}

// TestReadPrefersEXIFDateOverFilenameDate pins the tier order at the top: the
// filename heuristic is a fallback, never an override.
//
// The fixture is a 4x4 JPEG whose name says 2020-01-01 and whose EXIF says
// 2023-08-15, generated once with:
//
//	exiftool -overwrite_original -DateTimeOriginal="2023:08:15 12:00:00" \
//	  internal/exifmeta/testdata/IMG_20200101_000000.jpg
func TestReadPrefersEXIFDateOverFilenameDate(t *testing.T) {
	p, err := exifmeta.Read(filepath.Join("testdata", "IMG_20200101_000000.jpg"), photo.JPEG)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got, want := p.Taken.Format(wallClock), "2023-08-15 12:00:00"; got != want {
		t.Errorf("Taken = %s; want the EXIF date %s, not the date in the file name", got, want)
	}
}
