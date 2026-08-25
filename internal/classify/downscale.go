package classify

import (
	"bytes"
	"image"
	"image/jpeg"
	_ "image/png" // register PNG so image.Decode handles a PNG source

	xdraw "golang.org/x/image/draw"
)

const (
	// maxImageDim is the longest side, in pixels, of an image sent to the model.
	// A vision model downsamples its input to a few hundred pixels per tile
	// anyway, so a 12 MP original spends bandwidth and base64 padding (+33%) on
	// detail that is discarded before inference.
	maxImageDim = 1024
	// jpegQuality trades a little fidelity for a much smaller payload. At 85 the
	// artefacts are invisible to a classifier deciding "mountain or kitchen".
	jpegQuality = 85
	// maxImagePixels caps what shrink is willing to decode. image.Decode sizes its
	// pixel buffer from the dimensions the file declares about itself, and neither
	// image/jpeg nor image/png enforces a ceiling on those, so a few-KB header
	// claiming 65535×65535 is a request to allocate gigabytes — twice over, once in
	// the decoder and again in image.NewRGBA — from a file the scan was perfectly
	// happy to pick up. 128 MP sits above every single-shot consumer sensor (the
	// largest today is around 100 MP) and bounds the worst case at ~512 MB as RGBA.
	maxImagePixels = 128 << 20
)

// shrink returns data re-encoded as a JPEG no larger than maxImageDim on its long
// side, and reports whether the result should be sent to the model at all.
//
// Images already within the cap — and anything that cannot be decoded — come back
// untouched with ok true: shrinking is an optimisation, never a reason to drop a
// photo the model could otherwise have seen. That is why there is no error to
// return; there is nothing a caller could do about a failure here.
//
// ok false is the one exception, and it means the opposite: do not send this image.
// Either the header declares more than maxImagePixels, or a decoder panicked on the
// bytes. Both describe a corrupt or hostile file rather than a photo the model was
// ever going to make sense of, and both are cheaper to refuse than to survive.
//
// The long-side cap is on pixels, not on bytes, and deliberately so. A vision
// model tiles its input, so the dimensions decide how many image tokens it must
// encode and therefore how long inference takes; the smaller payload is the
// secondary prize. Re-encoding a highly compressible source (a screenshot, a flat
// graphic) can occasionally produce a larger file, and that is still the trade worth
// making.
func shrink(data []byte) (out []byte, ok bool) {
	// The decoders below parse bytes that arrived on a camera card or in a download,
	// on the look-ahead goroutine (internal/app.labelAhead) where a panic is nobody's
	// to recover. A crash in one of them must cost this photo its place in the
	// sample, not cost the run every photo it had not copied yet.
	defer func() {
		if state := recover(); state != nil {
			out, ok = nil, false
		}
	}()

	// The header first, and on its own: DecodeConfig reads the dimensions without
	// allocating a pixel for them, which is the only chance to refuse an image bomb
	// before the allocation it is asking for.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return data, true
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return data, true
	}
	// Widened deliberately: on a 32-bit build the int product of two large declared
	// dimensions overflows, and an overflowed comparison lets the bomb straight
	// through. This is the only test that has to hold against a hostile file.
	if int64(cfg.Width)*int64(cfg.Height) > maxImagePixels {
		return nil, false
	}
	if max(cfg.Width, cfg.Height) <= maxImageDim {
		return data, true
	}

	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return data, true
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 || max(w, h) <= maxImageDim {
		return data, true
	}

	dst := image.NewRGBA(image.Rect(0, 0, scaled(w, max(w, h)), scaled(h, max(w, h))))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Src, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return data, true
	}
	return buf.Bytes(), true
}

// scaled maps one side of an image through the ratio that brings its longest side
// down to maxImageDim, never below one pixel.
func scaled(side, longest int) int {
	n := side * maxImageDim / longest
	if n < 1 {
		return 1
	}
	return n
}
