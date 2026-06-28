package organize_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sgaunet/moraine/internal/organize"
)

// sidecarOrg returns an Organizer with companion copying enabled.
func sidecarOrg(dest string) *organize.Organizer {
	o := organize.New(dest)
	o.Sidecars = true
	return o
}

// splitResults separates photo Results from companion Results.
func splitResults(results []organize.Result) (photos, comps []organize.Result) {
	for _, r := range results {
		if r.IsCompanion {
			comps = append(comps, r)
		} else {
			photos = append(photos, r)
		}
	}
	return photos, comps
}

func TestMatchCompanion(t *testing.T) {
	tests := []struct {
		name       string
		photo      string
		candidate  string
		wantSuffix string
		wantKind   string
	}{
		{"appended xmp", "IMG.jpg", "IMG.jpg.xmp", ".xmp", "appended"},
		{"appended json", "IMG.jpg", "IMG.jpg.json", ".json", "appended"},
		{"appended numeric", "IMG.jpg", "IMG.jpg.001", ".001", "appended"},
		{"base xmp", "IMG.jpg", "IMG.xmp", "", "base"},
		{"base json", "IMG.jpg", "IMG.json", "", "base"},
		{"self", "IMG.jpg", "IMG.jpg", "", "none"},
		{"no extension", "IMG.jpg", "IMG", "", "none"},
		{"different stem with prefixish", "IMG.jpg", "IMG2.jpg.xmp", "", "none"},
		{"unrelated stem same ext", "IMG.jpg", "IMG_thumb.jpg", "", "none"},
		{"unrelated", "IMG.jpg", "OTHER.xmp", "", "none"},
		{"multidot appended", "my.photo.jpg", "my.photo.jpg.xmp", ".xmp", "appended"},
		{"multidot base", "my.photo.jpg", "my.photo.xmp", "", "base"},
		{"multidot shorter stem", "my.photo.jpg", "my.jpg", "", "none"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			suffix, kind := organize.MatchCompanion(tc.photo, tc.candidate)
			if kind != tc.wantKind || suffix != tc.wantSuffix {
				t.Fatalf("MatchCompanion(%q,%q) = (%q,%q); want (%q,%q)",
					tc.photo, tc.candidate, suffix, kind, tc.wantSuffix, tc.wantKind)
			}
		})
	}
}

func TestCompanionTargetName(t *testing.T) {
	tests := []struct {
		name      string
		finalName string
		candidate string
		suffix    string
		kind      string
		want      string
	}{
		{"appended, not renamed", "IMG.jpg", "IMG.jpg.xmp", ".xmp", "appended", "IMG.jpg.xmp"},
		{"base, not renamed", "IMG.jpg", "IMG.xmp", "", "base", "IMG.xmp"},
		{"appended, renamed photo", "IMG (1).jpg", "IMG.jpg.xmp", ".xmp", "appended", "IMG (1).jpg.xmp"},
		{"base, renamed photo", "IMG (1).jpg", "IMG.xmp", "", "base", "IMG (1).xmp"},
		{"appended json", "IMG.jpg", "IMG.jpg.json", ".json", "appended", "IMG.jpg.json"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := organize.CompanionTargetName(tc.finalName, tc.candidate, tc.suffix, tc.kind)
			if got != tc.want {
				t.Fatalf("CompanionTargetName = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestWithinDest(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "_sorted")
	tests := []struct {
		path string
		want bool
	}{
		{dest, true},
		{filepath.Join(dest, "a", "b.jpg"), true},
		{filepath.Join(base, "photo.jpg"), false},
		{filepath.Join(base, "_sortedX", "y.jpg"), false},
		{filepath.Join(base, "..", "elsewhere.jpg"), false},
	}
	for _, tc := range tests {
		if got := organize.WithinDest(tc.path, filepath.Clean(dest)); got != tc.want {
			t.Errorf("WithinDest(%q) = %v; want %v", tc.path, got, tc.want)
		}
	}
}

func TestPlaceCompanionsCopiesAppendedAndBaseName(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	c := clusterOf(t, src, "IMG.jpg")
	writeFile(t, filepath.Join(src, "IMG.jpg.xmp"), "appended-sidecar")
	writeFile(t, filepath.Join(src, "IMG.xmp"), "base-sidecar")

	photos, comps := splitResults(sidecarOrg(dest).Place(context.Background(), c, "nature"))
	if len(photos) != 1 || photos[0].Action != organize.ActionCopied {
		t.Fatalf("photo result = %+v", photos)
	}
	if len(comps) != 2 {
		t.Fatalf("want 2 companions, got %d: %+v", len(comps), comps)
	}
	destDir := filepath.Join(dest, "nature", "2025", "2025-08-12")
	for _, n := range []string{"IMG.jpg.xmp", "IMG.xmp"} {
		if _, err := os.Stat(filepath.Join(destDir, n)); err != nil {
			t.Errorf("companion %s not placed: %v", n, err)
		}
		if _, err := os.Stat(filepath.Join(src, n)); err != nil {
			t.Errorf("source companion %s must be preserved: %v", n, err)
		}
	}
	for _, r := range comps {
		if r.Of != filepath.Join(src, "IMG.jpg") {
			t.Errorf("companion not linked to photo: Of=%q", r.Of)
		}
		if r.Theme != "nature" {
			t.Errorf("companion theme = %q, want nature", r.Theme)
		}
	}
}

func TestPlaceCompanionsSkipIdentical(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	c := clusterOf(t, src, "IMG.jpg")
	writeFile(t, filepath.Join(src, "IMG.xmp"), "sidecar")

	o := sidecarOrg(dest)
	o.Place(context.Background(), c, "nature") // first run copies
	_, comps := splitResults(o.Place(context.Background(), c, "nature"))
	if len(comps) != 1 || comps[0].Action != organize.ActionSkippedIdentical {
		t.Fatalf("re-run: want skipped-identical companion, got %+v", comps)
	}
}

func TestPlaceCompanionsRenamesDifferentContent(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	c := clusterOf(t, src, "IMG.jpg")
	companion := filepath.Join(src, "IMG.xmp")
	writeFile(t, companion, "version-A")

	sidecarOrg(dest).Place(context.Background(), c, "nature") // places IMG.xmp = version-A
	writeFile(t, companion, "version-B-different")            // same target name, different bytes

	_, comps := splitResults(sidecarOrg(dest).Place(context.Background(), c, "nature"))
	if len(comps) != 1 || comps[0].Action != organize.ActionRenamed {
		t.Fatalf("want renamed companion, got %+v", comps)
	}
	if filepath.Base(comps[0].Dest) != "IMG (1).xmp" {
		t.Fatalf("want suffixed companion 'IMG (1).xmp', got %q", filepath.Base(comps[0].Dest))
	}
}

func TestPlaceCompanionsLinkPreservingOnPhotoRename(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	c := clusterOf(t, src, "IMG.jpg") // source content "content-IMG.jpg"
	writeFile(t, filepath.Join(src, "IMG.jpg.xmp"), "appended")
	writeFile(t, filepath.Join(src, "IMG.xmp"), "base")

	// A different file already occupies IMG.jpg at the destination → forces rename.
	destDir := filepath.Join(dest, "nature", "2025", "2025-08-12")
	writeFile(t, filepath.Join(destDir, "IMG.jpg"), "a-different-photo")

	photos, comps := splitResults(sidecarOrg(dest).Place(context.Background(), c, "nature"))
	if photos[0].Action != organize.ActionRenamed || filepath.Base(photos[0].Dest) != "IMG (1).jpg" {
		t.Fatalf("photo not renamed as expected: %+v", photos[0])
	}
	want := map[string]bool{"IMG (1).jpg.xmp": false, "IMG (1).xmp": false}
	for _, r := range comps {
		want[filepath.Base(r.Dest)] = true
	}
	for n, seen := range want {
		if !seen {
			t.Errorf("link-preserving companion %q missing; got %+v", n, comps)
		}
		if _, err := os.Stat(filepath.Join(destDir, n)); err != nil {
			t.Errorf("companion %q not on disk: %v", n, err)
		}
	}
}

func TestPlaceCompanionsExcludesPrimaries(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	c := clusterOf(t, src, "IMG.jpg")
	writeFile(t, filepath.Join(src, "IMG.xmp"), "ok")
	writeFile(t, filepath.Join(src, "IMG.dng"), "raw-is-a-primary")

	o := sidecarOrg(dest)
	o.IsPrimary = func(p string) bool { return filepath.Base(p) == "IMG.dng" }
	_, comps := splitResults(o.Place(context.Background(), c, "nature"))
	if len(comps) != 1 || filepath.Base(comps[0].Dest) != "IMG.xmp" {
		t.Fatalf("want only IMG.xmp companion, got %+v", comps)
	}
	if _, err := os.Stat(filepath.Join(dest, "nature", "2025", "2025-08-12", "IMG.dng")); !os.IsNotExist(err) {
		t.Error("a scanned primary must not be copied as a companion (FR-006)")
	}
}

func TestPlaceCompanionsExcludesDestTree(t *testing.T) {
	root := t.TempDir()
	c := clusterOf(t, root, "IMG.jpg")
	writeFile(t, filepath.Join(root, "IMG.xmp"), "sidecar")

	o := sidecarOrg(root) // DestRoot == the photo's own directory → siblings are under dest
	_, comps := splitResults(o.Place(context.Background(), c, "nature"))
	if len(comps) != 0 {
		t.Fatalf("companions under the destination tree must be excluded (FR-007), got %+v", comps)
	}
}

func TestPlaceCompanionsNoneWhenPhotoNotPlaced(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	c := clusterOf(t, src, "IMG.jpg")
	writeFile(t, filepath.Join(src, "IMG.xmp"), "sidecar")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, comps := splitResults(sidecarOrg(dest).Place(ctx, c, "nature"))
	if len(comps) != 0 {
		t.Fatalf("no companions when the photo was not placed, got %+v", comps)
	}
}

func TestPlaceCompanionsSharedBaseName(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	c := clusterOf(t, src, "IMG.jpg", "IMG.png") // two primaries sharing the stem "IMG"
	writeFile(t, filepath.Join(src, "IMG.xmp"), "shared-sidecar")

	o := sidecarOrg(dest)
	primaries := map[string]bool{
		filepath.Join(src, "IMG.jpg"): true,
		filepath.Join(src, "IMG.png"): true,
	}
	o.IsPrimary = func(p string) bool { return primaries[p] }

	photos, comps := splitResults(o.Place(context.Background(), c, "nature"))
	if len(photos) != 2 {
		t.Fatalf("want 2 photos, got %d", len(photos))
	}
	var copied, skipped int
	for _, r := range comps {
		switch r.Action {
		case organize.ActionCopied:
			copied++
		case organize.ActionSkippedIdentical:
			skipped++
		case organize.ActionRenamed:
		}
	}
	if copied != 1 || skipped != 1 {
		t.Fatalf("shared base-name companion: copied=%d skipped=%d (want 1/1); %+v", copied, skipped, comps)
	}
	if _, err := os.Stat(filepath.Join(dest, "nature", "2025", "2025-08-12", "IMG.xmp")); err != nil {
		t.Errorf("shared companion not placed: %v", err)
	}
}

func TestPlaceCompanionsDisabled(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	c := clusterOf(t, src, "IMG.jpg")
	writeFile(t, filepath.Join(src, "IMG.xmp"), "sidecar")

	_, comps := splitResults(organize.New(dest).Place(context.Background(), c, "nature")) // Sidecars=false
	if len(comps) != 0 {
		t.Fatalf("disabled sidecars must emit no companions, got %+v", comps)
	}
	if _, err := os.Stat(filepath.Join(dest, "nature", "2025", "2025-08-12", "IMG.xmp")); !os.IsNotExist(err) {
		t.Error("companion must not be copied when disabled (INV-2)")
	}
}

func TestPlaceCompanionsPlacedWhenPhotoSkippedIdentical(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	c := clusterOf(t, src, "IMG.jpg")
	writeFile(t, filepath.Join(src, "IMG.xmp"), "sidecar")

	organize.New(dest).Place(context.Background(), c, "nature") // sidecars OFF: archive only the photo

	photos, comps := splitResults(sidecarOrg(dest).Place(context.Background(), c, "nature"))
	if photos[0].Action != organize.ActionSkippedIdentical {
		t.Fatalf("photo should be skipped-identical, got %s", photos[0].Action)
	}
	if len(comps) != 1 || comps[0].Action != organize.ActionCopied {
		t.Fatalf("companion must be placed even when the photo is skipped-identical (FR-005), got %+v", comps)
	}
	if _, err := os.Stat(filepath.Join(dest, "nature", "2025", "2025-08-12", "IMG.xmp")); err != nil {
		t.Errorf("companion not placed: %v", err)
	}
}

func TestPlaceCompanionFailureIsNonFatal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are ignored")
	}
	src := t.TempDir()
	dest := t.TempDir()
	c := clusterOf(t, src, "IMG.jpg")
	bad := filepath.Join(src, "IMG.xmp")
	writeFile(t, bad, "sidecar")
	if err := os.Chmod(bad, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0o644) })

	photos, comps := splitResults(sidecarOrg(dest).Place(context.Background(), c, "nature"))
	if photos[0].Err != nil || photos[0].Action != organize.ActionCopied {
		t.Fatalf("photo must still be placed despite a companion failure (FR-008): %+v", photos[0])
	}
	if len(comps) != 1 || comps[0].Err == nil {
		t.Fatalf("want one errored companion result, got %+v", comps)
	}
}
