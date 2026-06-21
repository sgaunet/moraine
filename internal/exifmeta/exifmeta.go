// Package exifmeta extracts capture metadata (date, GPS, altitude) from image
// files using the pure-Go imagemeta library. When EXIF is missing or
// unreadable, the file's modification time is used as a fallback date so a
// photo is never silently dropped (FR-002, Assumptions).
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
// itself is fatal; a missing/unparsable EXIF block is not (date falls back to
// mtime, GPS/altitude stay nil).
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
		// No / unreadable EXIF — fall back to the file mtime.
		p.Taken = mtime
		return p, nil
	}

	taken := ex.SelectedDate()
	if taken.IsZero() {
		taken = mtime
	}
	p.Taken = taken

	if lat, lng := ex.GPS.Latitude(), ex.GPS.Longitude(); lat != 0 || lng != 0 {
		p.GPS = &photo.LatLng{Lat: lat, Lng: lng}
		alt := float64(ex.GPS.Altitude())
		p.Altitude = &alt
	}
	return p, nil
}
