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
)

// shrink returns data re-encoded as a JPEG no larger than maxImageDim on its long
// side. Images already within the cap — and anything that cannot be decoded — are
// returned untouched: shrinking is an optimisation, never a reason to drop a photo
// the model could otherwise have seen. That is why no error is returned; there is
// nothing a caller could do about a failure here.
//
// The cap is on pixels, not on bytes, and deliberately so. A vision model tiles
// its input, so the dimensions decide how many image tokens it must encode and
// therefore how long inference takes; the smaller payload is the secondary prize.
// Re-encoding a highly compressible source (a screenshot, a flat graphic) can
// occasionally produce a larger file, and that is still the trade worth making.
func shrink(data []byte) []byte {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return data
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 || max(w, h) <= maxImageDim {
		return data
	}

	dst := image.NewRGBA(image.Rect(0, 0, scaled(w, max(w, h)), scaled(h, max(w, h))))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Src, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return data
	}
	return buf.Bytes()
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
