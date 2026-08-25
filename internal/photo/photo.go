// Package photo holds the core domain types produced by the scan/EXIF/cluster
// pipeline. It has no dependency on transport or storage (Constitution Principle III).
package photo

import (
	"path/filepath"
	"sort"
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
	// HEIC covers .heic and .heif. Pixels are not decoded in pure Go; a
	// model-viewable preview is extracted via exiftool, like RAW.
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
	case FormatUnknown:
		return "unknown"
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

// NeedsPreview reports whether the format's pixels cannot be decoded in pure Go,
// so a model-viewable image has to be extracted with exiftool rather than read
// from the file itself. That is exactly the complement of Decodable among the
// recognised formats: RAW and HEIC.
func (f Format) NeedsPreview() bool {
	return f == RAW || f == HEIC
}

// extFormats maps a lower-cased, dot-prefixed file extension to its Format. It
// is the single source of truth for which files moraine recognises, shared by
// FormatFromExt and Extensions.
var extFormats = map[string]Format{
	".jpg":  JPEG,
	".jpeg": JPEG,
	".png":  PNG,
	".heic": HEIC,
	".heif": HEIC,
	".dng":  RAW,
	".nef":  RAW,
	".cr2":  RAW,
	".cr3":  RAW,
	".arw":  RAW,
	".raf":  RAW,
	".rw2":  RAW,
	".orf":  RAW,
	".pef":  RAW,
	".srw":  RAW,
}

// FormatFromExt maps a file name (or extension) to a recognised Format.
// Matching is case-insensitive. The boolean is false for unsupported files.
func FormatFromExt(name string) (Format, bool) {
	format, ok := extFormats[strings.ToLower(filepath.Ext(name))]
	if !ok {
		return FormatUnknown, false
	}
	return format, true
}

// Extensions returns every recognised file extension, dot-prefixed, lower-cased
// and sorted. Callers that need a stable list (shell completion) use this rather
// than restating the set.
func Extensions() []string {
	exts := make([]string, 0, len(extFormats))
	for ext := range extFormats {
		exts = append(exts, ext)
	}
	sort.Strings(exts)
	return exts
}

// LatLng is a geographic coordinate in decimal degrees.
type LatLng struct {
	Lat float64
	Lng float64
}

// Photo is one scanned file plus the metadata read from it: the output of the
// scan and exifmeta stages, and the input to clustering.
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
