package organize

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sgaunet/moraine/internal/photo"
)

// Action records what happened when placing one photo.
type Action string

const (
	// ActionCopied means the photo was copied to a free destination path.
	ActionCopied Action = "copied"
	// ActionSkippedIdentical means a byte-identical file already existed; nothing was written.
	ActionSkippedIdentical Action = "skipped-identical"
	// ActionRenamed means a same-named but different file existed; the photo was copied under a suffixed name.
	ActionRenamed Action = "renamed"
)

// Result is the outcome of placing one file (a photo or one of its companions).
type Result struct {
	Source      string    // absolute source path
	Dest        string    // absolute destination path actually targeted (after any suffix)
	Theme       string    // theme slug used
	Date        time.Time // representative date used for <year>/<date>
	Action      Action    // copied | skipped-identical | renamed
	Err         error     // non-nil on a placement failure (run continues)
	IsCompanion bool      // true ⇒ this Result is a sidecar of a photo (see Of)
	Of          string    // owning photo's source path, when IsCompanion
	// Moved reports that the source file was removed after its copy was verified.
	// It is what tells `undo` to leave this copy alone: the original is gone, so
	// removing the copy would destroy the only remaining file.
	Moved bool
	// Size is the file's size in bytes: what was written for a copy or a rename, and
	// what did not have to be written again for a skip. It is 0 on a failure, and on
	// a dry run it is what the run would have written. Together with Action it is
	// what lets the summary report the volume copied and the volume spared.
	Size int64
}

// Placement is what an earlier run recorded about a file it placed: where the copy
// went and the size and modification time it was left with. Because a copy carries
// the source's modification time, the same pair also fingerprints the source.
type Placement struct {
	Dest    string
	Size    int64
	ModTime time.Time
}

// Organizer copies photos (and, when Sidecars is set, their companion files)
// under a destination root, in the layout its Template describes (by default
// <theme>/<year>/<year-month-day>/).
type Organizer struct {
	DestRoot string
	// Template is the destination path layout. The zero Template renders
	// DefaultTemplate, the layout moraine has always used, so a caller that never
	// sets one is unaffected.
	Template Template
	// Sidecars enables copying each photo's companion (sidecar) files into the
	// same destination folder as the photo.
	Sidecars bool
	// Move removes each source file once its copy has been read back and verified —
	// never on a skip, an error, a cancellation or a dry run. Verification, not the
	// absence of a write error, is the precondition: see copy and verifyCopy.
	Move bool
	// DryRun reports what a run would do without writing anything — no file, and
	// not even a destination directory. Every Result still carries the Action the
	// real run would take, so a preview and the run it previews agree.
	DryRun bool
	// Placed, when set, reports what an earlier run recorded for a source path.
	// A hit whose fingerprints still match on both ends lets an incremental run
	// skip a file without reading either copy — the byte comparison a normal run
	// does is precisely what it replaces. Injected by the caller (from the run
	// manifest) to keep this package free of any manifest dependency; nil ⇒ every
	// file is compared as usual.
	Placed func(src string) (Placement, bool)
	// IsPrimary reports whether an absolute source path is itself a scanned
	// primary photo, so it is never also copied as another photo's companion
	// (FR-006). Injected by the caller to keep this package decoupled from the
	// scanner; nil ⇒ "never primary".
	IsPrimary func(absPath string) bool
	// dirEntries caches one os.ReadDir result per source directory so companion
	// discovery stays linear (one listing per directory). Place runs sequentially,
	// so no synchronisation is needed.
	dirEntries map[string][]os.DirEntry
	// afterPublish, when set, runs between publishing a copy and verifying it. That
	// window is the only place a corrupt destination can be simulated, so it is the
	// seam the verify-failure test uses; installed via export_test.go and always nil
	// in production.
	afterPublish func(dst string)
	// planned holds the destination paths a dry run has already promised to create.
	// Only a dry run populates it: a real run writes its files, so the filesystem
	// itself records what is taken. Without it two same-named photos in one run
	// would both be previewed as landing on the same path.
	planned map[string]struct{}
}

// New builds an Organizer writing under destRoot.
func New(destRoot string) *Organizer {
	return &Organizer{DestRoot: destRoot}
}

// Place copies every photo of the cluster into DestRoot/<Template>/<name>, using
// the cluster's representative date (c.Start) for all photos. It returns one Result
// per photo. A failure on one photo is recorded in its Result.Err and does not abort
// the others.
func (o *Organizer) Place(ctx context.Context, c photo.Cluster, theme string) []Result {
	results := make([]Result, 0, len(c.Photos))
	date := c.Start
	dirOf := o.lazyDir(theme, date)

	for _, p := range c.Photos {
		if err := ctx.Err(); err != nil {
			results = append(results, Result{Source: p.Path, Theme: theme, Date: date, Err: err})
			continue
		}
		res := Result{Source: p.Path, Theme: theme, Date: date}
		res.Dest, res.Action, res.Size, res.Moved, res.Err = o.placeOne(dirOf, p.Path, p.Name)
		results = append(results, res)

		// Bring the photo's companion (sidecar) files along, for any successful
		// placement action (copied/skipped-identical/renamed). They inherit the
		// photo's theme and date and a name that tracks its final placed name.
		if o.Sidecars && res.Err == nil {
			comps := o.placeCompanions(dirOf, p.Path, filepath.Base(res.Dest))
			for i := range comps {
				comps[i].Theme = theme
				comps[i].Date = date
			}
			results = append(results, comps...)
		}
	}
	return results
}

// lazyDir returns a memoised accessor for the cluster's destination directory.
// Creating it on first use rather than up front is what keeps an incremental re-run
// from leaving a new empty folder behind for a cluster whose every file is already
// placed. Memoising means one cluster still creates the directory exactly once, and
// a failure is reported identically to every file that needed it.
func (o *Organizer) lazyDir(theme string, date time.Time) func() (string, error) {
	var (
		dir  string
		err  error
		done bool
	)
	return func() (string, error) {
		if !done {
			dir, err = o.dir(theme, date)
			done = true
		}
		return dir, err
	}
}

// dir builds the destination directory for a theme and date, creating it unless
// this is a dry run — a preview must not leave empty folders behind either. The
// path is still resolved through safeJoin, so traversal is rejected in both modes
// even though ParseTemplate has already rejected it when the flag was parsed.
func (o *Organizer) dir(theme string, date time.Time) (string, error) {
	dir, err := safeJoin(o.DestRoot, o.Template.Render(theme, date))
	if err != nil {
		return "", err
	}
	if o.DryRun {
		return dir, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating directory %q: %w", dir, err)
	}
	return dir, nil
}

// taken reports whether a destination path is already claimed — by a file on disk
// or, in a dry run, by an earlier placement of this same run.
func (o *Organizer) taken(path string) bool {
	if exists(path) {
		return true
	}
	_, planned := o.planned[path]
	return planned
}

// claim records a destination path as used. It is a no-op outside a dry run, where
// the file written to that path speaks for itself.
func (o *Organizer) claim(path string) {
	if !o.DryRun {
		return
	}
	if o.planned == nil {
		o.planned = make(map[string]struct{})
	}
	o.planned[path] = struct{}{}
}

// placeOne copies a single source file into dir, resolving collisions: an
// identical existing file is skipped, a same-named different file is suffixed. In
// a dry run it resolves the same Action but writes nothing.
//
// It also reports the file's size — the bytes written for a copy or a rename, and the
// bytes a skip did not have to write again. A copy takes the count straight from
// io.Copy; the skip paths cost one extra stat, which is negligible beside the content
// comparison they already do — and whether the source was removed, which under --move
// happens for a verified copy or rename and for nothing else.
func (o *Organizer) placeOne(
	dirOf func() (string, error), src, name string,
) (dest string, action Action, size int64, moved bool, err error) {
	if d, size, ok := o.alreadyPlaced(src); ok {
		// A skip verified nothing this run — the incremental check deliberately never
		// reads the bytes — so --move never removes a source it merely recognised.
		return d, ActionSkippedIdentical, size, false, nil
	}
	// Resolved here, not by the caller: a file the manifest already accounts for
	// needs no destination directory, so an incremental re-run creates none.
	dir, err := dirOf()
	if err != nil {
		return "", "", 0, false, err
	}
	target := filepath.Join(dir, name)
	if o.taken(target) {
		// Only a real file has content to compare; a dry run's own planned target
		// is a name reservation, so it falls straight through to the rename.
		if exists(target) {
			identical, err := sameContent(src, target)
			if err != nil {
				return target, "", 0, false, fmt.Errorf("comparing %q: %w", target, err)
			}
			if identical {
				return target, ActionSkippedIdentical, fileSize(target), false, nil
			}
			// A previous run may already have placed this exact content under a
			// " (N)" name. Without this check every re-run would re-collide on the
			// original name and copy the same bytes again under the next free
			// suffix, so re-runs would not be idempotent (SC-002/SC-008).
			if placed, err := existingIdentical(dir, name, src); err != nil {
				return target, "", 0, false, fmt.Errorf("comparing collision variants of %q: %w", target, err)
			} else if placed != "" {
				d := filepath.Join(dir, placed)
				return d, ActionSkippedIdentical, fileSize(d), false, nil
			}
		}
		name = uniqueName(dir, name, o.taken)
		target = filepath.Join(dir, name)
		n, moved, err := o.copy(src, target)
		if err != nil {
			return target, "", 0, false, err
		}
		return target, ActionRenamed, n, moved, nil
	}
	n, moved, err := o.copy(src, target)
	if err != nil {
		return target, "", 0, false, err
	}
	return target, ActionCopied, n, moved, nil
}

// copy performs the placement, or merely reserves the destination name when this is
// a dry run. Routing every write through here is what makes "a dry run writes
// nothing" a property of one line rather than a rule each caller must remember — and
// the same now holds for "only a verified copy removes its source", since this is
// also the only place a source is ever deleted.
func (o *Organizer) copy(src, dst string) (int64, bool, error) {
	o.claim(dst)
	if o.DryRun {
		// A preview writes nothing, so there is no io.Copy count to report; the
		// source's own size is what the real run would have written. It removes
		// nothing either, so moved is false however Move is set.
		return fileSize(src), false, nil
	}
	n, sum, err := copyFile(src, dst)
	if err != nil {
		return 0, false, err
	}
	if !o.Move {
		return n, false, nil
	}
	if o.afterPublish != nil {
		o.afterPublish(dst)
	}
	if err := verifyCopy(dst, sum, n); err != nil {
		return 0, false, err
	}
	if err := os.Remove(src); err != nil {
		return 0, false, fmt.Errorf("removing source %q after a verified copy: %w", src, err)
	}
	return n, true, nil
}

// alreadyPlaced reports the destination and size an earlier run recorded for src,
// when that record can still be trusted: the source must be unchanged since it was placed
// and its copy must still be on disk unchanged. Either end failing the check falls
// through to the normal path, so a stale or partly-undone manifest can only ever
// cost the skip — never correctness.
func (o *Organizer) alreadyPlaced(src string) (string, int64, bool) {
	if o.Placed == nil {
		return "", 0, false
	}
	rec, ok := o.Placed(src)
	if !ok || rec.Dest == "" {
		return "", 0, false
	}
	if !fingerprintMatches(src, rec) || !fingerprintMatches(rec.Dest, rec) {
		return "", 0, false
	}
	// The recorded size is the file's size, already verified against both ends by
	// fingerprintMatches, so the skipped volume costs nothing extra to report.
	return rec.Dest, rec.Size, true
}

// fingerprintMatches reports whether the regular file at path still has the size
// and modification time the placement recorded.
func fingerprintMatches(path string, rec Placement) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return info.Size() == rec.Size && info.ModTime().Equal(rec.ModTime)
}
