package rawpreview_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/exiftooltest"
	"github.com/sgaunet/moraine/internal/rawpreview"
)

func stubExtractor(t *testing.T, opts exiftooltest.Options) *rawpreview.Extractor {
	t.Helper()
	path, err := exiftooltest.Stub(t.TempDir(), opts)
	if err != nil {
		t.Fatalf("building exiftool stub: %v", err)
	}
	return rawpreview.NewExtractor(path, 5*time.Second)
}

func TestExtractPrefersLargestPreview(t *testing.T) {
	ex := stubExtractor(t, exiftooltest.Options{Previews: map[string][]byte{
		"JpgFromRaw":     []byte("FULL"),
		"PreviewImage":   []byte("PREVIEW"),
		"ThumbnailImage": []byte("THUMB"),
	}})
	got, err := ex.Extract(context.Background(), "shot.dng")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if string(got) != "FULL" {
		t.Errorf("got %q; want the JpgFromRaw preview %q", got, "FULL")
	}
}

func TestExtractFallsBackThroughTags(t *testing.T) {
	tests := []struct {
		name    string
		preview map[string][]byte
		want    string
	}{
		{"only preview-image", map[string][]byte{"PreviewImage": []byte("PREVIEW")}, "PREVIEW"},
		{"only thumbnail", map[string][]byte{"ThumbnailImage": []byte("THUMB")}, "THUMB"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ex := stubExtractor(t, exiftooltest.Options{Previews: tc.preview})
			got, err := ex.Extract(context.Background(), "shot.nef")
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("got %q; want %q", got, tc.want)
			}
		})
	}
}

func TestExtractNoPreview(t *testing.T) {
	ex := stubExtractor(t, exiftooltest.Options{}) // no previews configured
	_, err := ex.Extract(context.Background(), "shot.cr2")
	if !errors.Is(err, rawpreview.ErrNoPreview) {
		t.Fatalf("err = %v; want ErrNoPreview", err)
	}
}

func TestExtractTimeout(t *testing.T) {
	path, err := exiftooltest.Stub(t.TempDir(), exiftooltest.Options{
		Previews: map[string][]byte{"JpgFromRaw": []byte("FULL")},
		SleepMS:  500,
	})
	if err != nil {
		t.Fatal(err)
	}
	ex := rawpreview.NewExtractor(path, 30*time.Millisecond)
	_, err = ex.Extract(context.Background(), "shot.dng")
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if errors.Is(err, rawpreview.ErrNoPreview) {
		t.Errorf("timeout must not be reported as ErrNoPreview: %v", err)
	}
}

func TestExtractBinaryMissingIsHardError(t *testing.T) {
	ex := rawpreview.NewExtractor("/no/such/exiftool-binary", time.Second)
	_, err := ex.Extract(context.Background(), "shot.dng")
	if err == nil {
		t.Fatal("expected an error when exiftool cannot start")
	}
	if errors.Is(err, rawpreview.ErrNoPreview) {
		t.Errorf("a missing binary must not be ErrNoPreview: %v", err)
	}
}

// TestExtractWritesNoTempFiles verifies the in-memory guarantee (FR-005, SC-003):
// Extract must not create any file under the OS temp dir, on success or ErrNoPreview.
func TestExtractWritesNoTempFiles(t *testing.T) {
	// Build the stubs first (they use t.TempDir under the original temp root),
	// then redirect TMPDIR to a fresh, monitored dir so any stray temp write
	// by Extract would land there.
	withPreview := stubExtractor(t, exiftooltest.Options{Previews: map[string][]byte{
		"JpgFromRaw": []byte("FULL"),
	}})
	noPreview := stubExtractor(t, exiftooltest.Options{})
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	if _, err := withPreview.Extract(context.Background(), "shot.dng"); err != nil {
		t.Fatalf("Extract (success): %v", err)
	}
	if _, err := noPreview.Extract(context.Background(), "shot.dng"); !errors.Is(err, rawpreview.ErrNoPreview) {
		t.Fatalf("Extract (no preview): %v", err)
	}

	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("temp dir not empty after Extract: %v (previews must stay in memory)", entries)
	}
}
