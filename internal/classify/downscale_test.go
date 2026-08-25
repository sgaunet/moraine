package classify_test

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"sync"
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

// shrunk asserts that shrink was willing to send this image and returns what it
// would send. Every test below that cares about pixels also cares that the image
// was not refused outright.
func shrunk(t *testing.T, data []byte) []byte {
	t.Helper()
	out, ok := classify.Shrink(data)
	if !ok {
		t.Fatalf("shrink refused a %d-byte image it should have sent", len(data))
	}
	return out
}

func TestShrinkCapsTheLongSideAndTheBytes(t *testing.T) {
	original := encodeJPEG(t, gradient(1400, 840))

	got := shrunk(t, original)

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
	got := shrunk(t, encodeJPEG(t, gradient(600, 1600)))

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
	if got := shrunk(t, original); !bytes.Equal(got, original) {
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

	got := shrunk(t, buf.Bytes())

	if w, h := decodeDims(t, got); w != classify.MaxImageDim || h != classify.MaxImageDim {
		t.Errorf("PNG shrunk to %dx%d; want %d on both sides", w, h, classify.MaxImageDim)
	}
}

func TestShrinkReturnsUndecodableBytesUnchanged(t *testing.T) {
	// Resizing is an optimisation. Anything Go cannot decode is still worth
	// sending: the model may well understand it.
	original := []byte("not an image at all")
	if got := shrunk(t, original); !bytes.Equal(got, original) {
		t.Errorf("undecodable bytes were altered: %q", got)
	}
}

// pngHeaderOnly builds the first two chunks of a PNG — the signature and an IHDR
// declaring the given dimensions — and nothing else. That is all image.DecodeConfig
// reads, and it is exactly the shape of an "image bomb": a handful of bytes whose
// header asks the decoder to allocate a buffer far larger than the file.
func pngHeaderOnly(w, h uint32) []byte {
	var ihdr bytes.Buffer
	ihdr.WriteString("IHDR")
	_ = binary.Write(&ihdr, binary.BigEndian, w)
	_ = binary.Write(&ihdr, binary.BigEndian, h)
	ihdr.Write([]byte{8, 2, 0, 0, 0}) // 8-bit truecolour, no interlacing

	var out bytes.Buffer
	out.WriteString("\x89PNG\r\n\x1a\n")
	_ = binary.Write(&out, binary.BigEndian, uint32(ihdr.Len()-len("IHDR")))
	out.Write(ihdr.Bytes())
	_ = binary.Write(&out, binary.BigEndian, crc32.ChecksumIEEE(ihdr.Bytes()))
	return out.Bytes()
}

// jpegHeaderOnly builds SOI + a JFIF APP0 + an SOF0 declaring the given dimensions.
// image/jpeg's DecodeConfig returns as soon as it has seen a JFIF marker and a frame
// header, so this is a complete input for it — and a JPEG is the likelier bomb of the
// two, since it is what a camera and a messaging app both produce.
func jpegHeaderOnly(w, h uint16) []byte {
	var out bytes.Buffer
	out.Write([]byte{0xFF, 0xD8})                   // SOI
	out.Write([]byte{0xFF, 0xE0, 0x00, 0x10})       // APP0, length 16
	out.WriteString("JFIF\x00")                     // identifier
	out.Write([]byte{1, 1, 0, 0, 1, 0, 1, 0, 0})    // version, units, densities, no thumbnail
	out.Write([]byte{0xFF, 0xC0, 0x00, 0x0B, 0x08}) // SOF0, length 11, 8-bit precision
	_ = binary.Write(&out, binary.BigEndian, h)
	_ = binary.Write(&out, binary.BigEndian, w)
	out.Write([]byte{0x01, 0x01, 0x11, 0x00}) // one component
	return out.Bytes()
}

func TestShrinkRefusesAnImageBomb(t *testing.T) {
	// 65535x65535 is 4.3 gigapixels: image.Decode would try to allocate tens of
	// gigabytes for it, from a file of a few dozen bytes. Refusing is the point, and
	// `ok == false` is what proves shrink stopped at the header — bytes it merely
	// cannot decode come back unchanged with ok true (see the test above).
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"png", pngHeaderOnly(65535, 65535)},
		{"jpeg", jpegHeaderOnly(65535, 65535)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Guard the premise: the stdlib really does hand these dimensions over
			// without complaint, which is the whole reason the cap has to exist here.
			cfg, _, err := image.DecodeConfig(bytes.NewReader(tc.data))
			if err != nil {
				t.Fatalf("DecodeConfig rejected the crafted header: %v", err)
			}
			if cfg.Width != 65535 || cfg.Height != 65535 {
				t.Fatalf("crafted header decoded as %dx%d; want 65535x65535", cfg.Width, cfg.Height)
			}

			out, ok := classify.Shrink(tc.data)
			if ok {
				t.Errorf("shrink accepted a %d-pixel image; want it refused", cfg.Width*cfg.Height)
			}
			if out != nil {
				t.Errorf("shrink returned %d bytes for a refused image; want nil", len(out))
			}
		})
	}
}

func TestShrinkAcceptsAnImageJustUnderThePixelCap(t *testing.T) {
	// The cap must not be so eager that it refuses a real photograph. A header at
	// the limit is accepted and, having no pixel data behind it, comes back as the
	// undecodable bytes it is — the point being that shrink got past the cap.
	const height = 8192
	data := pngHeaderOnly(uint32(classify.MaxImagePixels/height), height)
	if out, ok := classify.Shrink(data); !ok {
		t.Errorf("shrink refused an image of exactly the cap (%d pixels)", classify.MaxImagePixels)
	} else if !bytes.Equal(out, data) {
		t.Errorf("an undecodable header was altered: %d bytes out of %d in", len(out), len(data))
	}
}

// panicMagic is the file signature of a format registered below whose decoder
// panics on purpose. It is deliberately unlike any real signature, so registering
// it globally (image.RegisterFormat has no other mode) cannot shadow JPEG or PNG.
const panicMagic = "\x00moraine-panic\x00"

// registerPanicFormat installs that format exactly once however many times the
// test runs in one binary — the suite runs with -count=2, and image.RegisterFormat
// appends rather than replaces.
var registerPanicFormat = sync.OnceFunc(func() {
	image.RegisterFormat("moraine-panic-test", panicMagic,
		func(io.Reader) (image.Image, error) { panic("decoder blew up on a malformed image") },
		func(io.Reader) (image.Config, error) {
			// Large enough to be worth resizing, small enough to clear the pixel cap,
			// so the panicking Decode below is what the test actually reaches.
			return image.Config{ColorModel: color.RGBAModel, Width: 4000, Height: 3000}, nil
		})
})

func TestShrinkRefusesAnImageWhoseDecoderPanics(t *testing.T) {
	// The real risk this guards is a stdlib or third-party decoder crashing on a
	// crafted file. That happens on the look-ahead goroutine, where a panic ends the
	// whole run, so shrink converts it into one skipped photo instead.
	registerPanicFormat()

	out, ok := classify.Shrink([]byte(panicMagic + "and then some payload"))
	if ok {
		t.Error("shrink accepted an image whose decoder panicked; want it refused")
	}
	if out != nil {
		t.Errorf("shrink returned %d bytes after a decoder panic; want nil", len(out))
	}
}
