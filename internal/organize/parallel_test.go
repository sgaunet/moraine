package organize_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/organize"
	"github.com/sgaunet/moraine/internal/photo"
)

// collidingCluster builds n photos that all share one file name but hold different
// content, each in a source directory of its own. clusterOf cannot express this:
// it derives content from the name, so photos sharing a name are byte-identical.
// Distinct content is what forces the ` (N)` collision path for every photo.
func collidingCluster(t *testing.T, name string, n int) photo.Cluster {
	t.Helper()
	date := time.Date(2025, 8, 12, 10, 0, 0, 0, time.UTC)
	ps := make([]photo.Photo, 0, n)
	for i := range n {
		p := filepath.Join(t.TempDir(), name)
		writeFile(t, p, fmt.Sprintf("content-%s-%d", name, i))
		ps = append(ps, photo.Photo{
			Path: p, Name: name,
			Taken:  date.Add(time.Duration(i) * time.Minute),
			Format: photo.JPEG,
		})
	}
	return photo.Cluster{Photos: ps, Start: date, End: date}
}

// placements renders results as an ordered "action name" list, which is what a
// comparison between two runs has to agree on: the action, the final name, and the
// order the records came out in.
func placements(results []organize.Result) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		if r.Err != nil {
			out = append(out, "error "+filepath.Base(r.Source))
			continue
		}
		out = append(out, string(r.Action)+" "+filepath.Base(r.Dest))
	}
	return out
}

func listNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// TestPlaceAssignsTheSameNamesAtEveryWorkerCount is the determinism gate for the
// parallel copy stage. Which photo keeps the un-suffixed name is decided by the
// cluster's total order (capture time, then path) and nothing else — never by which
// worker happened to finish first. Without it, ` (N)` becomes a race and the
// destination paths on stdout stop being reproducible.
func TestPlaceAssignsTheSameNamesAtEveryWorkerCount(t *testing.T) {
	run := func(t *testing.T, jobs int) ([]string, []string) {
		t.Helper()
		dest := t.TempDir()
		c := collidingCluster(t, "IMG.jpg", 12)
		o := organize.New(dest)
		o.Jobs = jobs
		results := o.Place(context.Background(), c, "nature")
		return placements(results), listNames(t, filepath.Join(dest, "nature", "2025", "2025-08-12"))
	}

	serialPlacements, serialNames := run(t, 1)
	for _, jobs := range []int{2, 8, 16} {
		gotPlacements, gotNames := run(t, jobs)
		if !slices.Equal(gotPlacements, serialPlacements) {
			t.Errorf("jobs=%d changed the placements:\n got %v\nwant %v", jobs, gotPlacements, serialPlacements)
		}
		if !slices.Equal(gotNames, serialNames) {
			t.Errorf("jobs=%d changed the destination listing:\n got %v\nwant %v", jobs, gotNames, serialNames)
		}
	}
	// The first photo in cluster order keeps the plain name; the rest are suffixed
	// in order, with no gaps.
	if serialPlacements[0] != "copied IMG.jpg" {
		t.Errorf("first placement = %q; want %q", serialPlacements[0], "copied IMG.jpg")
	}
	for i := 1; i < len(serialPlacements); i++ {
		if want := fmt.Sprintf("renamed IMG (%d).jpg", i); serialPlacements[i] != want {
			t.Errorf("placement %d = %q; want %q", i, serialPlacements[i], want)
		}
	}
}

// TestPlaceKeepsVariantIndicesContiguous pins the invariant existingIdentical
// depends on: uniqueName always fills the first free index, so occupied ` (N)`
// indices have no gaps and a re-run's scan can stop at the first one. A concurrent
// allocator that punched a hole would silently break re-run idempotence.
func TestPlaceKeepsVariantIndicesContiguous(t *testing.T) {
	dest := t.TempDir()
	const n = 64
	o := organize.New(dest)
	o.Jobs = 16
	results := o.Place(context.Background(), collidingCluster(t, "IMG.jpg", n), "nature")

	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("placement failed: %v", r.Err)
		}
	}
	want := make([]string, 0, n)
	want = append(want, "IMG.jpg")
	for i := 1; i < n; i++ {
		want = append(want, fmt.Sprintf("IMG (%d).jpg", i))
	}
	sort.Strings(want)
	if got := listNames(t, filepath.Join(dest, "nature", "2025", "2025-08-12")); !slices.Equal(got, want) {
		t.Errorf("variant indices are not contiguous:\n got %v\nwant %v", got, want)
	}
	// A crash-safe copy stages a hidden temp file; none may survive a clean run.
	for _, name := range listNames(t, filepath.Join(dest, "nature", "2025", "2025-08-12")) {
		if strings.HasPrefix(name, ".moraine-") {
			t.Errorf("temporary file left behind: %q", name)
		}
	}
}

// TestPlaceDedupsIdenticalPhotosWithinOneRun pins that two byte-identical photos
// sharing a name are still copied once and skipped once, at any worker count and in
// a dry run alike. Deferring the writes means the first photo's copy is not on disk
// when the second is resolved, so the comparison has to reach the source that
// reserved the name instead. Getting this wrong would copy the same bytes twice
// under a ` (N)` name.
func TestPlaceDedupsIdenticalPhotosWithinOneRun(t *testing.T) {
	// Same name, same content, different directories.
	newCluster := func(t *testing.T) photo.Cluster {
		t.Helper()
		date := time.Date(2025, 8, 12, 10, 0, 0, 0, time.UTC)
		ps := make([]photo.Photo, 0, 2)
		for i := range 2 {
			p := filepath.Join(t.TempDir(), "IMG.jpg")
			writeFile(t, p, "the-very-same-bytes")
			ps = append(ps, photo.Photo{
				Path: p, Name: "IMG.jpg",
				Taken:  date.Add(time.Duration(i) * time.Minute),
				Format: photo.JPEG,
			})
		}
		return photo.Cluster{Photos: ps, Start: date, End: date}
	}

	for _, tc := range []struct {
		name   string
		jobs   int
		dryRun bool
	}{
		{"serial", 1, false},
		{"concurrent", 8, false},
		{"dry run", 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dest := t.TempDir()
			o := organize.New(dest)
			o.Jobs, o.DryRun = tc.jobs, tc.dryRun
			results := o.Place(context.Background(), newCluster(t), "nature")

			if len(results) != 2 {
				t.Fatalf("results = %d; want 2", len(results))
			}
			if results[0].Action != organize.ActionCopied {
				t.Errorf("first photo = %s; want copied", results[0].Action)
			}
			if results[1].Action != organize.ActionSkippedIdentical {
				t.Errorf("second photo = %s; want skipped-identical (the bytes are already going there)",
					results[1].Action)
			}
			if results[1].Dest != results[0].Dest {
				t.Errorf("the skip points at %q; want the copy's own destination %q",
					results[1].Dest, results[0].Dest)
			}
			if tc.dryRun {
				if n := entryCount(t, dest); n != 0 {
					t.Errorf("dry run created %d entries; want none", n)
				}
				return
			}
			if got := listNames(t, filepath.Join(dest, "nature", "2025", "2025-08-12")); len(got) != 1 {
				t.Errorf("destination holds %v; want a single file", got)
			}
		})
	}
}

// TestPlaceDedupsAgainstAVariantReservedThisRun is the same question one level
// deeper. existingIdentical walks the ` (N)` variants looking for content already
// placed; a variant this run has promised but not yet written is a gap in that walk,
// so without consulting the reservations the scan stops early and copies the bytes
// again under the next free suffix.
func TestPlaceDedupsAgainstAVariantReservedThisRun(t *testing.T) {
	for _, jobs := range []int{1, 8} {
		t.Run(fmt.Sprintf("jobs=%d", jobs), func(t *testing.T) {
			dest := t.TempDir()
			dir := filepath.Join(dest, "nature", "2025", "2025-08-12")
			// Something different already occupies the plain name, so both photos of
			// the cluster must take a suffix — and they are identical to each other.
			writeFile(t, filepath.Join(dir, "IMG.jpg"), "someone-elses-photo")

			date := time.Date(2025, 8, 12, 10, 0, 0, 0, time.UTC)
			ps := make([]photo.Photo, 0, 2)
			for i := range 2 {
				p := filepath.Join(t.TempDir(), "IMG.jpg")
				writeFile(t, p, "identical-twins")
				ps = append(ps, photo.Photo{
					Path: p, Name: "IMG.jpg",
					Taken:  date.Add(time.Duration(i) * time.Minute),
					Format: photo.JPEG,
				})
			}
			o := organize.New(dest)
			o.Jobs = jobs
			results := o.Place(context.Background(), photo.Cluster{Photos: ps, Start: date, End: date}, "nature")

			if got := placements(results); !slices.Equal(got, []string{"renamed IMG (1).jpg", "skipped-identical IMG (1).jpg"}) {
				t.Errorf("placements = %v; want the second photo to recognise the first's reservation", got)
			}
			if got := listNames(t, dir); !slices.Equal(got, []string{"IMG (1).jpg", "IMG.jpg"}) {
				t.Errorf("destination = %v; want no IMG (2).jpg", got)
			}
		})
	}
}

// TestPlaceFailedPhotoWritePlacesNoCompanions guards a boundary the two-phase split
// could quietly move. A photo whose write fails places no companions at all, so the
// run reports one error rather than one per sidecar. Companion names are resolved
// before any bytes are written, so the failure has to travel back to them.
func TestPlaceFailedPhotoWritePlacesNoCompanions(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	src, dest := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "IMG.jpg"), "photo")
	writeFile(t, filepath.Join(src, "IMG.jpg.xmp"), "sidecar-a")
	writeFile(t, filepath.Join(src, "IMG.aae"), "sidecar-b")

	dir := filepath.Join(dest, "nature", "2025", "2025-08-12")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil { // readable, not writable
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	date := time.Date(2025, 8, 12, 10, 0, 0, 0, time.UTC)
	c := photo.Cluster{
		Photos: []photo.Photo{{
			Path: filepath.Join(src, "IMG.jpg"), Name: "IMG.jpg", Taken: date, Format: photo.JPEG,
		}},
		Start: date, End: date,
	}
	o := organize.New(dest)
	o.Sidecars = true
	results := o.Place(context.Background(), c, "nature")

	if len(results) != 1 {
		t.Fatalf("results = %d (%v); want exactly 1: a photo that could not be written places no companions",
			len(results), placements(results))
	}
	if results[0].Err == nil {
		t.Error("the photo's placement should have failed")
	}
	if results[0].IsCompanion {
		t.Error("the single result must be the photo, not a companion")
	}
}

// TestPlaceReservationsDoNotLeakBetweenRuns pins that a real run's name
// reservations end with it. They exist only because the writes are deferred inside
// one Place; afterwards the files are on disk and speak for themselves, and a name
// whose write failed is genuinely free again. Holding them would make a second run
// resolve a stale reservation to a source that has since changed.
func TestPlaceReservationsDoNotLeakBetweenRuns(t *testing.T) {
	dest := t.TempDir()
	src := t.TempDir()
	p := filepath.Join(src, "IMG.jpg")
	writeFile(t, p, "first-generation")
	date := time.Date(2025, 8, 12, 10, 0, 0, 0, time.UTC)
	c := photo.Cluster{
		Photos: []photo.Photo{{Path: p, Name: "IMG.jpg", Taken: date, Format: photo.JPEG}},
		Start:  date, End: date,
	}

	o := organize.New(dest)
	if got := o.Place(context.Background(), c, "nature"); got[0].Action != organize.ActionCopied {
		t.Fatalf("first run = %s; want copied", got[0].Action)
	}
	// Same path, new bytes: the second run must compare against the file on disk,
	// not against a reservation still pointing at this very source path.
	writeFile(t, p, "second-generation")
	got := o.Place(context.Background(), c, "nature")
	if got[0].Action != organize.ActionRenamed {
		t.Fatalf("second run = %s; want renamed — a stale reservation would report a false skip", got[0].Action)
	}
	if base := filepath.Base(got[0].Dest); base != "IMG (1).jpg" {
		t.Errorf("second run dest = %q; want 'IMG (1).jpg'", base)
	}
}

// TestPlaceCancelledMidCopyLeavesTheRestNotAttempted pins the interrupt contract
// through the worker pool: a photo the run never reached carries the context error
// and nothing else, so app.notAttempted keeps it out of every tally, and its source
// is still on disk. Which photos got through is timing-dependent, so this asserts
// the invariant rather than a count.
func TestPlaceCancelledMidCopyLeavesTheRestNotAttempted(t *testing.T) {
	for _, jobs := range []int{1, 8} {
		t.Run(fmt.Sprintf("jobs=%d", jobs), func(t *testing.T) {
			dest := t.TempDir()
			c := collidingCluster(t, "IMG.jpg", 24)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			o := organize.New(dest)
			o.Jobs, o.Move = jobs, true // Move is the only mode that runs afterPublish
			var once sync.Once
			organize.SetAfterPublish(o, func(string) { once.Do(cancel) })

			results := o.Place(ctx, c, "nature")
			if len(results) != len(c.Photos) {
				t.Fatalf("results = %d; want one per photo (%d)", len(results), len(c.Photos))
			}
			var reached int
			for _, r := range results {
				switch {
				case r.Err == nil:
					reached++
					if r.Dest == "" || r.Action == "" {
						t.Errorf("a placed photo is incomplete: %+v", r)
					}
				case errors.Is(r.Err, context.Canceled):
					// Not attempted: no destination was chosen and nothing was moved.
					if r.Dest != "" || r.Action != "" || r.Moved {
						t.Errorf("an unreached photo carries placement detail: %+v", r)
					}
					if _, err := os.Stat(r.Source); err != nil {
						t.Errorf("an unreached source was removed: %v", err)
					}
				default:
					t.Errorf("unexpected placement error: %v", r.Err)
				}
			}
			if reached == 0 {
				t.Error("nothing was placed before the cancellation; the test proves nothing")
			}
		})
	}
}
