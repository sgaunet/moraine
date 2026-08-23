// Package exifmeta extracts capture metadata (date, GPS, altitude) from image
// files using the pure-Go imagemeta library. The capture date is resolved in three
// tiers — EXIF, then the date encoded in the file name, then the file's
// modification time — so a photo is never silently dropped (FR-002, Assumptions).
// The filename tier sits above mtime because a batch of downloads shares one mtime
// but keeps its individual capture dates in its names (see filename.go).
//
// Time frame invariant: every Photo.Taken this package returns is a UTC-naive
// wall clock — the wall-clock fields the camera (or the filesystem) recorded,
// stamped in time.UTC. Capture dates are wall clocks, not instants: EXIF
// DateTimeOriginal is the camera's local reading, and the day a photo belongs to
// is its wall-clock day. Normalising every source into one frame is what makes
// the cluster gap arithmetic meaningful — mixing a UTC-naive EXIF date with a
// zone-carrying mtime instant skews the difference by the UTC offset (up to
// ±14h), which can split or merge events at the EXIF/mtime boundary.
package exifmeta

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/evanoberholster/imagemeta"
	"github.com/sgaunet/moraine/internal/photo"
)

// Read builds a photo.Photo for the file at path. A read error on the file
// itself is fatal; a missing/unparsable EXIF block is not (the date falls back to
// the file name, then to mtime; GPS/altitude stay nil). The returned Taken is
// always UTC-naive wall clock (see the package doc).
func Read(path string, format photo.Format) (photo.Photo, error) {
	p := photo.Photo{
		Path:   path,
		Name:   filepath.Base(path),
		Format: format,
	}

	f, err := os.Open(path)
	if err != nil {
		return photo.Photo{}, fmt.Errorf("opening %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	mtime := time.Time{}
	if info, statErr := f.Stat(); statErr == nil {
		mtime = info.ModTime()
	}

	ex, err := imagemeta.Decode(f)
	if err != nil {
		// No / unreadable EXIF — fall back to the file name, then the mtime.
		p.Taken = wallClockUTC(fallbackDate(p.Name, mtime))
		return p, nil
	}

	taken := ex.SelectedDate()
	if taken.IsZero() {
		taken = fallbackDate(p.Name, mtime)
	}
	p.Taken = wallClockUTC(taken)

	if lat, lng := ex.GPS.Latitude(), ex.GPS.Longitude(); lat != 0 || lng != 0 {
		p.GPS = &photo.LatLng{Lat: lat, Lng: lng}
		alt := float64(ex.GPS.Altitude())
		p.Altitude = &alt
	}
	return p, nil
}

// fallbackDate returns the date to use when EXIF carries none: the date encoded in
// the file name when there is one, else the file's modification time.
func fallbackDate(name string, mtime time.Time) time.Time {
	if t := dateFromName(name); !t.IsZero() {
		return t
	}
	return mtime
}

// wallClockUTC re-stamps t's own wall-clock fields in time.UTC, projecting every
// source onto the single frame described in the package doc. A zero time stays
// zero. It reads t in t's own location, so:
//
//   - an EXIF date without OffsetTimeOriginal is already UTC-naive: unchanged;
//   - an EXIF date with an offset keeps the camera's local reading, so the photo
//     lands on the day the shutter actually fired there;
//   - a filename date is built in UTC already: unchanged;
//   - an mtime (a real instant in time.Local) keeps the local reading it would
//     have been formatted with anyway, so destination folders do not change.
func wallClockUTC(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	y, mo, d := t.Date()
	h, mi, s := t.Clock()
	return time.Date(y, mo, d, h, mi, s, t.Nanosecond(), time.UTC)
}
