package classify_test

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/sgaunet/moraine/internal/classify"
)

// gradient builds an image with real detail, so JPEG cannot compress it to
// nothing and the byte counts below mean something. It fills the pixel buffer
// directly: an img.Set per pixel is several instrumented memory accesses under
// -race, and these images are megapixels large.
func gradient(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		row := img.Pix[y*img.Stride : y*img.Stride+w*4]
		for x := range w {
			px := row[x*4 : x*4+4 : x*4+4]
			px[0], px[1], px[2], px[3] = uint8(x), uint8(y), uint8(x+y), 0xff
		}
	}
	return img
}

// encodeJPEG encodes at the same quality shrink re-encodes with, so a byte
// comparison isolates the effect of dropping pixels rather than mixing in a
// change of quality setting.
func encodeJPEG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func decodeDims(t *testing.T, data []byte) (int, int) {
	t.Helper()
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decoding the shrunk image: %v", err)
	}
	return cfg.Width, cfg.Height
}

func TestShrinkCapsTheLongSideAndTheBytes(t *testing.T) {
	original := encodeJPEG(t, gradient(1400, 840))

	got := classify.Shrink(original)

	w, h := decodeDims(t, got)
	if w != classify.MaxImageDim {
		t.Errorf("width = %d; want %d (the long side is capped)", w, classify.MaxImageDim)
	}
	if want := 840 * classify.MaxImageDim / 1400; h != want {
		t.Errorf("height = %d; want %d (aspect ratio preserved)", h, want)
	}
	if len(got) >= len(original) {
		t.Errorf("shrunk to %d bytes from %d; want smaller", len(got), len(original))
	}
}

func TestShrinkCapsAPortraitImage(t *testing.T) {
	// The cap applies to whichever side is longest, not to the width.
	got := classify.Shrink(encodeJPEG(t, gradient(600, 1600)))

	w, h := decodeDims(t, got)
	if h != classify.MaxImageDim {
		t.Errorf("height = %d; want %d", h, classify.MaxImageDim)
	}
	if want := 600 * classify.MaxImageDim / 1600; w != want {
		t.Errorf("width = %d; want %d", w, want)
	}
}

func TestShrinkLeavesSmallImagesUntouched(t *testing.T) {
	// Already within the cap: no re-encode, so no needless generation loss.
	original := encodeJPEG(t, gradient(300, 200))
	if got := classify.Shrink(original); !bytes.Equal(got, original) {
		t.Errorf("a %d-byte image within the cap was re-encoded to %d bytes", len(original), len(got))
	}
}

func TestShrinkAcceptsPNG(t *testing.T) {
	// A PNG source is decoded and re-encoded as JPEG like any other. Only the
	// pixel cap is asserted: a synthetic gradient compresses better as PNG than as
	// JPEG, and shrink promises dimensions, not bytes.
	var buf bytes.Buffer
	if err := png.Encode(&buf, gradient(1300, 1300)); err != nil {
		t.Fatal(err)
	}

	got := classify.Shrink(buf.Bytes())

	if w, h := decodeDims(t, got); w != classify.MaxImageDim || h != classify.MaxImageDim {
		t.Errorf("PNG shrunk to %dx%d; want %d on both sides", w, h, classify.MaxImageDim)
	}
}

func TestShrinkReturnsUndecodableBytesUnchanged(t *testing.T) {
	// Resizing is an optimisation. Anything Go cannot decode is still worth
	// sending: the model may well understand it.
	original := []byte("not an image at all")
	if got := classify.Shrink(original); !bytes.Equal(got, original) {
		t.Errorf("undecodable bytes were altered: %q", got)
	}
}
