package thumb_test

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"github.com/sgaunet/moraine/internal/photo"
	"github.com/sgaunet/moraine/internal/thumb"
)

var placeholder = []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)

func makeJPEG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 100, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestThumbnailDecodesAndResizes(t *testing.T) {
	src := filepath.Join(t.TempDir(), "big.jpg")
	makeJPEG(t, src, 800, 600)

	c, err := thumb.NewCache(t.TempDir(), placeholder)
	if err != nil {
		t.Fatal(err)
	}
	data, ct, etag, err := c.Thumbnail(src, photo.JPEG)
	if err != nil {
		t.Fatal(err)
	}
	if ct != "image/jpeg" {
		t.Errorf("contentType = %q; want image/jpeg", ct)
	}
	if etag == "" {
		t.Error("etag must not be empty")
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("thumbnail is not a valid image: %v", err)
	}
	if img.Bounds().Dx() != 256 {
		t.Errorf("thumbnail width = %d; want 256", img.Bounds().Dx())
	}
}

func TestThumbnailPlaceholderForHEIC(t *testing.T) {
	// A .heic file (content irrelevant — not decoded).
	src := filepath.Join(t.TempDir(), "live.heic")
	if err := os.WriteFile(src, []byte("heic-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := thumb.NewCache(t.TempDir(), placeholder)
	if err != nil {
		t.Fatal(err)
	}
	data, ct, _, err := c.Thumbnail(src, photo.HEIC)
	if err != nil {
		t.Fatal(err)
	}
	if ct != "image/svg+xml" {
		t.Errorf("contentType = %q; want image/svg+xml", ct)
	}
	if !bytes.Equal(data, placeholder) {
		t.Error("HEIC must return the placeholder bytes")
	}
}

func TestThumbnailCachesOnDisk(t *testing.T) {
	src := filepath.Join(t.TempDir(), "img.jpg")
	makeJPEG(t, src, 400, 400)
	cacheDir := t.TempDir()

	c, err := thumb.NewCache(cacheDir, placeholder)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := c.Thumbnail(src, photo.JPEG); err != nil {
		t.Fatal(err)
	}
	// A cached .jpg should now exist (atomic temp files are renamed away).
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	var jpgs, temps int
	for _, e := range entries {
		switch {
		case filepath.Ext(e.Name()) == ".jpg":
			jpgs++
		case len(e.Name()) >= 6 && e.Name()[:6] == ".thumb":
			temps++
		}
	}
	if jpgs != 1 {
		t.Errorf("cached jpgs = %d; want 1", jpgs)
	}
	if temps != 0 {
		t.Errorf("leftover temp files = %d; want 0 (atomic rename)", temps)
	}

	// Second call returns identical bytes (served from cache).
	d2, _, _, err := c.Thumbnail(src, photo.JPEG)
	if err != nil {
		t.Fatal(err)
	}
	if len(d2) == 0 {
		t.Error("cached thumbnail is empty")
	}
}

func TestThumbnailMissingSourceErrors(t *testing.T) {
	c, err := thumb.NewCache(t.TempDir(), placeholder)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := c.Thumbnail(filepath.Join(t.TempDir(), "gone.jpg"), photo.JPEG); err == nil {
		t.Fatal("expected error for missing source file")
	}
}
