// Package photo holds the core domain types produced by the scan/EXIF/cluster
// pipeline. It has no dependency on transport or storage (Constitution Principle III).
package photo

import (
	"path/filepath"
	"strings"
	"time"
)

// Format enumerates the image formats moraine recognises.
type Format int

const (
	// FormatUnknown is the zero value for an unrecognised extension.
	FormatUnknown Format = iota
	// JPEG covers .jpg and .jpeg.
	JPEG
	// PNG covers .png.
	PNG
	// HEIC covers .heic and .heif (metadata only — pixels not decoded).
	HEIC
	// RAW covers camera raw formats (.dng/.nef/.cr2/…). Metadata is read by
	// imagemeta; pixels are not decoded in pure Go — a model-viewable preview is
	// extracted via exiftool when needed (feature 003).
	RAW
)

// String returns a short lowercase name, useful for logs.
func (f Format) String() string {
	switch f {
	case JPEG:
		return "jpeg"
	case PNG:
		return "png"
	case HEIC:
		return "heic"
	case RAW:
		return "raw"
	default:
		return "unknown"
	}
}

// Decodable reports whether the format's pixels can be decoded by the pure-Go
// stdlib (for thumbnail generation). HEIC and RAW are not decodable.
func (f Format) Decodable() bool {
	return f == JPEG || f == PNG
}

// IsRAW reports whether the format is a camera RAW format. RAW pixels are not
// decoded in pure Go; a model-viewable preview is extracted via exiftool.
func (f Format) IsRAW() bool {
	return f == RAW
}

// FormatFromExt maps a file name (or extension) to a recognised Format.
// Matching is case-insensitive. The boolean is false for unsupported files.
func FormatFromExt(name string) (Format, bool) {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".jpg", ".jpeg":
		return JPEG, true
	case ".png":
		return PNG, true
	case ".heic", ".heif":
		return HEIC, true
	case ".dng", ".nef", ".cr2", ".cr3", ".arw", ".raf", ".rw2", ".orf", ".pef", ".srw":
		return RAW, true
	default:
		return FormatUnknown, false
	}
}

// LatLng is a geographic coordinate in decimal degrees.
type LatLng struct {
	Lat float64
	Lng float64
}

// Photo is the raw result of scanning a file and reading its metadata, before
// the server-side state (store) is built.
type Photo struct {
	Path     string    // absolute path within the source tree
	Name     string    // filepath.Base(Path)
	Taken    time.Time // capture time; falls back to file mtime when EXIF absent
	GPS      *LatLng   // nil when unavailable
	Altitude *float64  // metres; nil when unavailable
	Format   Format
}

// Cluster is a temporally-contiguous set of photos (output of clustering,
// input to classification). Photos are sorted by Taken ascending.
type Cluster struct {
	Photos []Photo
	Start  time.Time
	End    time.Time
}
