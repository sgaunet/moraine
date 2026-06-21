package classify

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"log/slog"
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
	oc := func(n int) *OllamaClassifier { return &OllamaClassifier{Sample: n, Logger: slog.Default()} }
	ctx := context.Background()

	if got := oc(3).sampleImages(ctx, mkCluster(2)); len(got) != 2 {
		t.Errorf("small group: sent %d images; want 2 (all)", len(got))
	}
	if got := oc(3).sampleImages(ctx, mkCluster(5)); len(got) != 3 {
		t.Errorf("large group: sent %d images; want 3 (sample)", len(got))
	}
	if got := oc(0).sampleImages(ctx, mkCluster(5)); got != nil {
		t.Errorf("sample 0: want nil, got %v", got)
	}
}

// fakeRaw is a non-nil RawExtractor so RAW photos become model-eligible; the
// returned bytes are irrelevant to choosePhotos (which only selects).
type fakeRaw struct{}

func (fakeRaw) Extract(context.Context, string) ([]byte, error) { return []byte("PREVIEW"), nil }

func countFormats(ps []photo.Photo) (jpegs, raws int) {
	for _, p := range ps {
		switch {
		case p.Format.Decodable():
			jpegs++
		case p.Format.IsRAW():
			raws++
		}
	}
	return
}

func TestChoosePhotosRAWEligibilityAndPreference(t *testing.T) {
	jpg := photo.Photo{Path: "j.jpg", Format: photo.JPEG}
	raw := photo.Photo{Path: "r.dng", Format: photo.RAW}
	heic := photo.Photo{Path: "h.heic", Format: photo.HEIC}
	rep := func(p photo.Photo, n int) []photo.Photo {
		out := make([]photo.Photo, n)
		for i := range out {
			out[i] = p
		}
		return out
	}

	t.Run("small mixed group uses every eligible incl RAW", func(t *testing.T) {
		o := &OllamaClassifier{Sample: 3, Raw: fakeRaw{}}
		got := o.choosePhotos(photo.Cluster{Photos: []photo.Photo{jpg, raw}}) // 2 ≤ 3
		j, r := countFormats(got)
		if j != 1 || r != 1 {
			t.Errorf("small group chose jpeg=%d raw=%d; want 1 and 1", j, r)
		}
	})

	t.Run("large group prefers JPEG, no RAW extracted when enough JPEG", func(t *testing.T) {
		o := &OllamaClassifier{Sample: 3, Raw: fakeRaw{}}
		photos := append(rep(jpg, 4), rep(raw, 2)...) // 6 photos > 3
		j, r := countFormats(o.choosePhotos(photo.Cluster{Photos: photos}))
		if j != 3 || r != 0 {
			t.Errorf("large group chose jpeg=%d raw=%d; want 3 and 0 (RAW avoided)", j, r)
		}
	})

	t.Run("large group fills sample with RAW when JPEG scarce", func(t *testing.T) {
		o := &OllamaClassifier{Sample: 3, Raw: fakeRaw{}}
		photos := append(rep(jpg, 1), rep(raw, 5)...) // 6 photos > 3
		j, r := countFormats(o.choosePhotos(photo.Cluster{Photos: photos}))
		if j != 1 || r != 2 {
			t.Errorf("large group chose jpeg=%d raw=%d; want 1 and 2 (filled with RAW)", j, r)
		}
	})

	t.Run("RAW ineligible without an extractor", func(t *testing.T) {
		o := &OllamaClassifier{Sample: 3} // Raw nil
		if got := o.choosePhotos(photo.Cluster{Photos: []photo.Photo{raw}}); got != nil {
			t.Errorf("RAW without extractor must be skipped; got %v", got)
		}
	})

	t.Run("HEIC excluded", func(t *testing.T) {
		o := &OllamaClassifier{Sample: 3, Raw: fakeRaw{}}
		if got := o.choosePhotos(photo.Cluster{Photos: []photo.Photo{heic}}); got != nil {
			t.Errorf("HEIC must be excluded; got %v", got)
		}
	})
}
