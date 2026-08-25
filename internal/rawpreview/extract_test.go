package rawpreview_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/exiftooltest"
	"github.com/sgaunet/moraine/internal/rawpreview"
)

func stubExtractor(t *testing.T, opts exiftooltest.Options) *rawpreview.Extractor {
	t.Helper()
	ex, _ := stubExtractorIn(t, opts)
	return ex
}

// stubExtractorIn also returns the stub's directory, so a test can ask it how many
// exiftool processes an Extract actually cost.
func stubExtractorIn(t *testing.T, opts exiftooltest.Options) (*rawpreview.Extractor, string) {
	t.Helper()
	dir := t.TempDir()
	path, err := exiftooltest.Stub(dir, opts)
	if err != nil {
		t.Fatalf("building exiftool stub: %v", err)
	}
	return rawpreview.NewExtractor(path, 5*time.Second), dir
}

// TestExtractCostsOneProcessPerPhoto is the regression test for issue #9's exiftool
// item: the tags used to be probed one exec.Command at a time, so a file answering
// only to the last of them cost three process spawns — and, because the timeout is
// per invocation, up to three times the budget the caller thought it had set.
func TestExtractCostsOneProcessPerPhoto(t *testing.T) {
	tests := []struct {
		name    string
		preview map[string][]byte
	}{
		// The worst case under the old loop: the winning tag is tried last.
		{"lowest-priority tag", map[string][]byte{"ThumbnailImage": []byte("THUMB")}},
		{"highest-priority tag", map[string][]byte{"JpgFromRaw": []byte("FULL")}},
		// No tag at all: the old loop still paid for all three probes.
		{"no preview at all", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ex, dir := stubExtractorIn(t, exiftooltest.Options{Previews: tc.preview})
			_, _ = ex.Extract(context.Background(), "shot.dng")
			got, err := exiftooltest.Invocations(dir)
			if err != nil {
				t.Fatal(err)
			}
			if got != 1 {
				t.Errorf("Extract spawned %d exiftool processes; want exactly 1", got)
			}
		})
	}
}

// TestExtractTimeoutBoundsTheWholeCall pins what the single invocation buys beyond
// speed: Timeout used to be applied per exec.Command, so probing three tags could
// take three times the bound and overrun the classifier's own call budget.
func TestExtractTimeoutBoundsTheWholeCall(t *testing.T) {
	path, err := exiftooltest.Stub(t.TempDir(), exiftooltest.Options{
		Previews: map[string][]byte{"ThumbnailImage": []byte("THUMB")}, // the last tag tried
		SleepMS:  200,
	})
	if err != nil {
		t.Fatal(err)
	}
	ex := rawpreview.NewExtractor(path, 100*time.Millisecond)

	start := time.Now()
	if _, err := ex.Extract(context.Background(), "shot.dng"); err == nil {
		t.Fatal("expected a timeout error")
	}
	// One invocation, so one timeout. Three would have taken past 300ms.
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Errorf("Extract took %v; the %v timeout must bound the whole call, not each tag",
			elapsed, 100*time.Millisecond)
	}
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

// TestExtractPassesThePathAsALiteralOperand is the regression test for issue #35's
// argv item: the photo path used to be the bare trailing argument, so a name
// starting with '-' would have been read as an option rather than a file. exiftool
// carries write-capable options (-@, -o, -tagsFromFile), which is why this argv in
// particular is worth pinning.
//
// Both guards are asserted: the "--" end-of-options marker exiftool documents, and
// the "./" prefix that makes the operand unmistakable even to a parser without one.
func TestExtractPassesThePathAsALiteralOperand(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string // how the path must reach exiftool
	}{
		{"a dash-leading name is neutralised", "-tagsFromFile.dng", "./-tagsFromFile.dng"},
		{"an ordinary relative path is untouched", "shot.dng", "shot.dng"},
		{"an absolute path is untouched", "/photos/shot.dng", "/photos/shot.dng"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ex, dir := stubExtractorIn(t, exiftooltest.Options{Previews: map[string][]byte{
				"JpgFromRaw": []byte("FULL"),
			}})
			if _, err := ex.Extract(context.Background(), tt.path); err != nil {
				t.Fatalf("Extract: %v", err)
			}

			got, err := exiftooltest.Args(dir)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"-json", "-b", "-JpgFromRaw", "-PreviewImage", "-ThumbnailImage", "--", tt.want}
			if !slices.Equal(got, want) {
				t.Errorf("exiftool argv =\n  %q\nwant\n  %q", got, want)
			}
		})
	}
}
