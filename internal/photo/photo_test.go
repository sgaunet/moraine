package photo_test

import (
	"testing"

	"github.com/sgaunet/moraine/internal/photo"
)

func TestFormatFromExt(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		want   photo.Format
		wantOK bool
	}{
		{"lowercase jpg", "IMG_1.jpg", photo.JPEG, true},
		{"jpeg", "IMG_1.jpeg", photo.JPEG, true},
		{"uppercase JPG", "IMG_1.JPG", photo.JPEG, true},
		{"mixed case Jpeg", "photo.JpEg", photo.JPEG, true},
		{"png", "shot.png", photo.PNG, true},
		{"uppercase PNG", "shot.PNG", photo.PNG, true},
		{"heic", "live.heic", photo.HEIC, true},
		{"heif", "live.HEIF", photo.HEIC, true},
		{"full path", "/a/b/c/IMG.jpg", photo.JPEG, true},
		{"unknown raw", "RAW.cr2", photo.FormatUnknown, false},
		{"unknown mp4", "clip.mp4", photo.FormatUnknown, false},
		{"no extension", "README", photo.FormatUnknown, false},
		{"empty", "", photo.FormatUnknown, false},
		{"dotfile no ext", ".gitignore", photo.FormatUnknown, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := photo.FormatFromExt(tc.input)
			if got != tc.want || ok != tc.wantOK {
				t.Fatalf("FormatFromExt(%q) = (%v, %v); want (%v, %v)",
					tc.input, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestFormatString(t *testing.T) {
	tests := map[photo.Format]string{
		photo.JPEG:          "jpeg",
		photo.PNG:           "png",
		photo.HEIC:          "heic",
		photo.FormatUnknown: "unknown",
	}
	for f, want := range tests {
		if got := f.String(); got != want {
			t.Errorf("%d.String() = %q; want %q", f, got, want)
		}
	}
}

func TestFormatDecodable(t *testing.T) {
	if !photo.JPEG.Decodable() || !photo.PNG.Decodable() {
		t.Error("JPEG and PNG must be decodable")
	}
	if photo.HEIC.Decodable() {
		t.Error("HEIC must not be decodable (placeholder path)")
	}
}
