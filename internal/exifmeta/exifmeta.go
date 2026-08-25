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
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/evanoberholster/imagemeta"
	"github.com/sgaunet/moraine/internal/photo"
)

// ErrEXIFPanic reports that the EXIF parser crashed on a file's metadata rather
// than returning an error for it. Read recovers the panic and returns a usable
// photo alongside this sentinel, so a caller can keep the file — a crash inside a
// metadata parser is no reason to leave a photo unorganised — and still say out
// loud which file provoked it.
var ErrEXIFPanic = errors.New("exif parser panicked")

// Read builds a photo.Photo for the file at path. A read error on the file
// itself is fatal; a missing/unparsable EXIF block is not (the date falls back to
// the file name, then to mtime; GPS/altitude stay nil). The returned Taken is
// always UTC-naive wall clock (see the package doc).
//
// A parser panic sits between the two. The photo comes back usable, dated by the
// same fallback tiers as any other unreadable EXIF block, but the error wraps
// ErrEXIFPanic: a caller that wants to keep the file can, and one that treats every
// error as fatal is no worse off than before. Both callers in internal/app keep it.
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

	d, err := decodeEXIF(f)
	if err != nil {
		// No / unreadable EXIF — fall back to the file name, then the mtime.
		p.Taken = wallClockUTC(fallbackDate(p.Name, mtime))
		if errors.Is(err, ErrEXIFPanic) {
			return p, fmt.Errorf("reading exif of %q: %w", path, err)
		}
		return p, nil
	}

	taken := d.taken
	if taken.IsZero() {
		taken = fallbackDate(p.Name, mtime)
	}
	p.Taken = wallClockUTC(taken)
	p.GPS, p.Altitude = d.gps, d.altitude
	return p, nil
}

// exifData is the part of a file's EXIF block that Read has any use for.
type exifData struct {
	taken    time.Time
	gps      *photo.LatLng
	altitude *float64
}

// decodeEXIF parses r's EXIF block, turning a panic in the third-party parser into
// an ErrEXIFPanic error instead of letting it take the process down.
//
// The boundary belongs here, at the point where moraine hands untrusted bytes to
// code it does not own, because the caller cannot supply one: Read runs on a worker
// pool (internal/app.readMeta), and a panic on a goroutine is not the spawner's to
// recover. imagemeta guards its own JPEG scanner (meta/jpeg/scanner.go), but only
// that one: the TIFF/DNG/NEF/CR2/ARW, HEIC/HEIF and PNG branches of imagemeta.Decode
// — and the imagetype sniff that runs ahead of all of them — read whatever came off
// the camera card unprotected. Without this, one malformed file ends a sort that may
// have been copying for many minutes.
//
// Reading the decoded value belongs inside the boundary too: those accessors walk
// parser state, so a malformed file can crash there just as easily as in Decode.
func decodeEXIF(r io.ReadSeeker) (d exifData, err error) {
	defer func() {
		if state := recover(); state != nil {
			d, err = exifData{}, fmt.Errorf("%w: %v", ErrEXIFPanic, state)
		}
	}()

	ex, err := imagemeta.Decode(r)
	if err != nil {
		return exifData{}, fmt.Errorf("decoding exif: %w", err)
	}

	d.taken = ex.SelectedDate()
	if lat, lng := ex.GPS.Latitude(), ex.GPS.Longitude(); lat != 0 || lng != 0 {
		d.gps = &photo.LatLng{Lat: lat, Lng: lng}
		alt := float64(ex.GPS.Altitude())
		d.altitude = &alt
	}
	return d, nil
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
