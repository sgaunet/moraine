package classify

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"github.com/sgaunet/moraine/internal/photo"
)

func TestEvenlySpaced(t *testing.T) {
	mk := func(n int) []photo.Photo {
		ps := make([]photo.Photo, n)
		for i := range ps {
			ps[i].Name = string(rune('0' + i))
		}
		return ps
	}
	tests := []struct {
		name string
		n    int
		size int
		want []string
	}{
		{"five pick three", 3, 5, []string{"0", "2", "4"}}, // first, middle, last
		{"pick one", 1, 5, []string{"0"}},
		{"n >= len returns all", 9, 3, []string{"0", "1", "2"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := evenlySpaced(mk(tc.size), tc.n)
			var names []string
			for _, p := range got {
				names = append(names, p.Name)
			}
			if len(names) != len(tc.want) {
				t.Fatalf("len = %d; want %d", len(names), len(tc.want))
			}
			for i := range names {
				if names[i] != tc.want[i] {
					t.Fatalf("got %v; want %v", names, tc.want)
				}
			}
		})
	}
}

func TestNormaliseTheme(t *testing.T) {
	set := []string{"family", "mountain", "special-events", "nature"}
	tests := map[string]string{
		"Mountain.":        "mountain",
		"  NATURE\n":       "nature",
		"special events":   "special-events",
		"special_events":   "special-events",
		"\"family\"":       "family",
		"concert":          "", // out of set
		"":                 "",
		"mountain hicking": "", // multi-word not in set
	}
	for in, want := range tests {
		if got := normaliseTheme(in, set); got != want {
			t.Errorf("normaliseTheme(%q) = %q; want %q", in, got, want)
		}
	}
}

func writeJPEG(t *testing.T, path string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 9, G: 9, B: 9, A: 255})
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSampleImagesSmallSendsAllLargeSamples(t *testing.T) {
	dir := t.TempDir()
	mkCluster := func(n int) photo.Cluster {
		var ps []photo.Photo
		for i := 0; i < n; i++ {
			p := filepath.Join(dir, "img"+string(rune('a'+i))+".jpg")
			writeJPEG(t, p)
			ps = append(ps, photo.Photo{Path: p, Format: photo.JPEG})
		}
		return photo.Cluster{Photos: ps}
	}

	if got := sampleImages(mkCluster(2), 3); len(got) != 2 {
		t.Errorf("small group: sent %d images; want 2 (all)", len(got))
	}
	if got := sampleImages(mkCluster(5), 3); len(got) != 3 {
		t.Errorf("large group: sent %d images; want 3 (sample)", len(got))
	}
	if got := sampleImages(mkCluster(5), 0); got != nil {
		t.Errorf("sample 0: want nil, got %v", got)
	}
}
