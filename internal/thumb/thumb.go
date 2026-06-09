// Package thumb generates and caches photo thumbnails. Decoding is pure-Go
// (image/jpeg, image/png); resizing uses golang.org/x/image/draw. Formats the
// stdlib cannot decode (HEIC) get a static SVG placeholder (R2/R3). Generated
// thumbnails are cached on disk with atomic writes so concurrent requests are
// safe without an application lock (R10).
package thumb

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // register PNG decoder
	"os"
	"path/filepath"

	"golang.org/x/image/draw"

	"github.com/sgaunet/moraine/internal/photo"
)

// thumbWidth is the target thumbnail width in pixels.
const thumbWidth = 256

// Cache generates thumbnails and caches them under Dir.
type Cache struct {
	dir         string
	placeholder []byte
}

// NewCache creates the cache directory and returns a Cache. placeholder is the
// SVG bytes served for non-decodable formats.
func NewCache(dir string, placeholder []byte) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("création du cache de vignettes %q: %w", dir, err)
	}
	return &Cache{dir: dir, placeholder: placeholder}, nil
}

// Thumbnail returns the thumbnail bytes, MIME type and a validator ETag for the
// photo at path. Non-decodable formats yield the SVG placeholder.
func (c *Cache) Thumbnail(path string, format photo.Format) (data []byte, contentType, etag string, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, "", "", fmt.Errorf("source illisible %q: %w", path, err)
	}
	etag = fmt.Sprintf(`"%d-%d"`, info.ModTime().UnixNano(), info.Size())

	if !format.Decodable() {
		return c.placeholder, "image/svg+xml", etag, nil
	}

	cachePath := filepath.Join(c.dir, sha1Hex(path)+".jpg")
	if ci, statErr := os.Stat(cachePath); statErr == nil && !ci.ModTime().Before(info.ModTime()) {
		if cached, readErr := os.ReadFile(cachePath); readErr == nil {
			return cached, "image/jpeg", etag, nil
		}
	}

	data, err = c.generate(path)
	if err != nil {
		return nil, "", "", err
	}
	// Best-effort cache write; failure to cache must not fail the request.
	_ = writeAtomic(cachePath, data)
	return data, "image/jpeg", etag, nil
}

// generate decodes the source image, resizes it and re-encodes it as JPEG.
func (c *Cache) generate(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("ouverture %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	src, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("décodage %q: %w", path, err)
	}

	dst := resize(src, thumbWidth)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 80}); err != nil {
		return nil, fmt.Errorf("encodage vignette: %w", err)
	}
	return buf.Bytes(), nil
}

// resize scales src down to width (preserving aspect ratio). Smaller images are
// left untouched (no upscaling) but still re-encoded by the caller.
func resize(src image.Image, width int) image.Image {
	b := src.Bounds()
	if b.Dx() <= width || b.Dx() == 0 {
		return src
	}
	height := max(b.Dy()*width/b.Dx(), 1)
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	return dst
}

// writeAtomic writes data to a temp file in the same dir then renames it into
// place, so readers never observe a partially-written thumbnail.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".thumb-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

func sha1Hex(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}
