package organize

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
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
	// Jobs bounds how many placements copy at once (0 ⇒ one per GOMAXPROCS). It is
	// the same --jobs the EXIF stage uses, for the same reason: turning it down
	// throttles a slow drive at either end of the copy, turning it up pays off on
	// fast local storage.
	Jobs int
	// OnUnitDone, when set, is called once for every photo Place actually reached —
	// copied, skipped, renamed or failed — so a caller can report progress. The unit
	// is the photo together with its companions, matching the unit of concurrent work,
	// so a photo with four sidecars still counts once.
	//
	// A photo a cancellation beat is NOT reported: it was never attempted, and a
	// progress report is no more entitled to count it than Summary is.
	//
	// It is called from the copy workers, so it MUST be safe for concurrent use, and
	// it is advisory only: the Results this returns keep the deterministic order a
	// serial run produced, which is what the stdout contract rests on.
	OnUnitDone func()
	// dirEntries caches one os.ReadDir result per source directory so companion
	// discovery stays linear (one listing per directory). Companion discovery happens
	// in Place's planning phase, which is sequential, so no synchronisation is needed.
	dirEntries map[string][]os.DirEntry
	// afterPublish, when set, runs between publishing a copy and verifying it. That
	// window is the only place a corrupt destination can be simulated, so it is the
	// seam the verify-failure test uses; installed via export_test.go and always nil
	// in production.
	afterPublish func(dst string)
	// reserved maps a destination path this run has promised to create to the source
	// whose bytes will be published there. Because Place resolves every name before
	// it copies anything, the copy is not on disk when the next file of the cluster
	// is resolved, and the reserving source is the only readable file holding the
	// bytes that are going there. Recording *what* will be at a path rather than
	// merely *that* something will be is what keeps two identical same-named photos
	// reported as copy + skipped-identical instead of copy + rename.
	//
	// Place clears it on return outside a dry run: by then the files are on disk and
	// exists answers for them, and a name whose write failed is genuinely free again.
	// A dry run keeps it, because nothing is ever written to speak for itself.
	reserved map[string]string
}

// New builds an Organizer writing under destRoot.
func New(destRoot string) *Organizer {
	return &Organizer{DestRoot: destRoot}
}

// Place copies every photo of the cluster into DestRoot/<Template>/<name>, using
// the cluster's representative date (c.Start) for all photos. It returns one Result
// per photo, followed by that photo's companions when Sidecars is set. A failure on
// one photo is recorded in its Result.Err and does not abort the others.
//
// It runs in two phases. Every destination name is resolved first, on one goroutine,
// in the cluster's own order; only then are the bytes copied, by a pool of Jobs
// workers. The split is what lets the copies run concurrently without the naming
// becoming a race: which photo keeps the un-suffixed name is decided by the total
// order cluster.Cluster establishes and by nothing else, the " (N)" indices stay
// contiguous for existingIdentical to walk, and the returned slice is in exactly the
// order a serial run produced — which the stdout contract depends on.
//
// The caller keeps every tally, the manifest and the result callback on its own
// goroutine, since Place returns whole clusters; nothing in this package is shared
// with a concurrent Place of another cluster.
func (o *Organizer) Place(ctx context.Context, c photo.Cluster, theme string) []Result {
	if !o.DryRun {
		// The reservations exist only for the window between resolving a name and
		// writing it, which closes here.
		defer func() { o.reserved = nil }()
	}
	results, units := o.plan(ctx, c, theme)
	o.execute(ctx, units, results)
	return compact(results, units)
}

// filePlan is one resolved placement: the Result as far as it can be decided without
// writing, plus whether a write is still owed. Reusing Result rather than a parallel
// struct is deliberate — everything the placement will report is already decided by
// the time the plan exists, except the two things only a write can produce.
type filePlan struct {
	res   Result
	write bool
}

// photoPlan is one unit of concurrent work: a photo together with the companions
// that follow it into the same folder. The pair is the unit rather than the file,
// for two reasons that are both properties of the pair — a companion's destination
// name tracks the photo's final placed name, and a photo whose write fails places no
// companions at all.
type photoPlan struct {
	photo      filePlan
	companions []filePlan
	base       int // index of the photo's Result in the results slice
	n          int // live Results from base: 1+len(companions), or 1 if the photo failed
}

// idle reports whether the unit owes no writes, so a cluster the manifest already
// accounts for spins up no goroutines at all.
func (u *photoPlan) idle() bool {
	if u.photo.write {
		return false
	}
	for _, c := range u.companions {
		if c.write {
			return false
		}
	}
	return true
}

// plan resolves every destination name for the cluster, in cluster order, on one
// goroutine. It returns the Results as far as they can be decided plus the units of
// work still owing bytes. Everything that reads or writes shared Organizer state —
// the directory cache, the reservations, the memoised destination directory —
// happens here and nowhere else.
func (o *Organizer) plan(ctx context.Context, c photo.Cluster, theme string) ([]Result, []photoPlan) {
	date := c.Start
	dirOf := o.lazyDir(theme, date)
	results := make([]Result, 0, len(c.Photos))
	units := make([]photoPlan, 0, len(c.Photos))

	for _, p := range c.Photos {
		base := len(results)
		if err := ctx.Err(); err != nil {
			results = append(results, Result{Source: p.Path, Theme: theme, Date: date, Err: err})
			units = append(units, photoPlan{base: base, n: 1})
			continue
		}
		pl := o.resolveOne(dirOf, p.Path, p.Name)
		pl.res.Theme, pl.res.Date = theme, date
		results = append(results, pl.res)
		unit := photoPlan{photo: pl, base: base, n: 1}

		// Bring the photo's companion (sidecar) files along, for any placement that
		// resolved successfully. They inherit the photo's theme and date and a name
		// that tracks its final placed name.
		if o.Sidecars && pl.res.Err == nil {
			unit.companions = o.planCompanions(dirOf, p.Path, filepath.Base(pl.res.Dest), theme, date)
			for _, comp := range unit.companions {
				results = append(results, comp.res)
			}
			unit.n = 1 + len(unit.companions)
		}
		units = append(units, unit)
	}
	return results, units
}

// execute copies the bytes every planned placement still owes, Jobs at a time. It
// follows the shape the EXIF stage established: a buffered channel as the semaphore,
// taken before the goroutine starts so at most that many exist at once, and a
// WaitGroup to join them.
//
// No lock and no re-ordering afterwards: each unit owns a contiguous, disjoint
// stretch of results, so the workers never touch the same element.
func (o *Organizer) execute(ctx context.Context, units []photoPlan, results []Result) {
	sem := make(chan struct{}, workerCount(o.Jobs))
	var wg sync.WaitGroup
	for i := range units {
		u := &units[i]
		if u.idle() {
			// Nothing to write, but the unit is done: an --incremental re-run that
			// skips everything must still advance a caller's progress to the end.
			//
			// Not after a cancellation, though. plan records a photo it never reached
			// as an idle unit too, and counting those would let an interrupted run
			// report a full bar — the mistake notAttempted exists to avoid in the
			// tally, which a progress report is no more entitled to make.
			if ctx.Err() == nil {
				o.unitDone()
			}
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(u *photoPlan) {
			defer wg.Done()
			defer func() { <-sem }()
			o.executeUnit(ctx, u, results)
		}(u)
	}
	wg.Wait()
}

// unitDone reports one finished unit, if anyone is listening.
func (o *Organizer) unitDone() {
	if o.OnUnitDone != nil {
		o.OnUnitDone()
	}
}

// executeUnit writes one photo and then its companions. A photo that could not be
// written places no companions: the run reports the photo's failure once rather than
// once more per sidecar that was never going to be written.
func (o *Organizer) executeUnit(ctx context.Context, u *photoPlan, results []Result) {
	if err := ctx.Err(); err != nil {
		// Recorded exactly as the planning phase records a photo it never reached:
		// source, theme and date, and no placement detail at all. Deliberately not
		// counted as progress — see the idle branch in execute.
		results[u.base] = unreached(u.photo.res, err)
		u.n = 1
		return
	}
	// Counted here rather than around the call, so a unit the cancellation reached
	// first advances nothing: it was never attempted.
	defer o.unitDone()
	if u.photo.write {
		results[u.base] = o.executeOne(u.photo)
		if results[u.base].Err != nil {
			u.n = 1
			return
		}
	}
	for i, comp := range u.companions {
		if comp.write {
			results[u.base+1+i] = o.executeOne(comp)
		}
	}
}

// executeOne performs one planned copy and completes its Result with the two things
// only the write can report: the bytes copied, and whether the source was removed.
func (o *Organizer) executeOne(pl filePlan) Result {
	res := pl.res
	n, moved, err := o.copy(res.Source, res.Dest)
	if err != nil {
		// The destination is kept on the Result — it says where the run was trying to
		// put the file — but the action is cleared: nothing happened.
		res.Action, res.Size, res.Moved, res.Err = "", 0, false, err
		return res
	}
	res.Size, res.Moved = n, moved
	return res
}

// unreached is the Result of a file the run never got to.
func unreached(r Result, err error) Result {
	return Result{Source: r.Source, Theme: r.Theme, Date: r.Date, Err: err}
}

// compact drops the companion slots of photos whose write failed, in place and in
// order. Companions are planned before it is known whether their photo can be
// written, so their slots are reserved and then given up.
func compact(results []Result, units []photoPlan) []Result {
	out := results[:0]
	for _, u := range units {
		out = append(out, results[u.base:u.base+u.n]...)
	}
	return out
}

// workerCount is how many placements copy at once: jobs, or one per GOMAXPROCS when
// jobs is 0. It mirrors the EXIF stage's sizing so one --jobs means one thing.
func workerCount(jobs int) int {
	if jobs < 1 {
		jobs = runtime.GOMAXPROCS(0)
	}
	if jobs < 1 {
		jobs = 1
	}
	return jobs
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

// contentAt returns the path holding the bytes that will be at path when the run
// finishes, and whether anything will be there at all. A destination this run has
// already reserved answers with the *source* that reserved it: the copy is not on
// disk yet, but by construction it will hold exactly that source's bytes.
//
// This is the one place that knows the writes are deferred, and every collision
// decision goes through it — which is what keeps a preview and the run it previews
// answering the same question about the same pair of files.
func (o *Organizer) contentAt(path string) (string, bool) {
	if src, ok := o.reserved[path]; ok {
		return src, true // reservations are only ever taken on names nothing else holds
	}
	if exists(path) {
		return path, true
	}
	return "", false
}

// taken reports whether a destination path is already spoken for — by a file on
// disk, or by an earlier placement of this same run.
func (o *Organizer) taken(path string) bool {
	_, ok := o.contentAt(path)
	return ok
}

// reserve records that src's bytes are going to dst.
func (o *Organizer) reserve(dst, src string) {
	if o.reserved == nil {
		o.reserved = make(map[string]string)
	}
	o.reserved[dst] = src
}

// resolveOne decides where a single source file goes and what will happen to it,
// resolving collisions: an identical file already there (or already going there) is
// skipped, a same-named different file is suffixed. It reserves the name it settles
// on, so the next file of the cluster sees it, and reports whether bytes are still
// owed. It writes nothing.
//
// The size it reports is the bytes a skip did not have to write again; a copy's size
// is only known once io.Copy has counted it, so that is filled in by executeOne.
func (o *Organizer) resolveOne(dirOf func() (string, error), src, name string) filePlan {
	res := Result{Source: src}
	if d, size, ok := o.alreadyPlaced(src); ok {
		// A skip verified nothing this run — the incremental check deliberately never
		// reads the bytes — so --move never removes a source it merely recognised.
		res.Dest, res.Action, res.Size = d, ActionSkippedIdentical, size
		return filePlan{res: res}
	}
	// Resolved here, not by the caller: a file the manifest already accounts for
	// needs no destination directory, so an incremental re-run creates none.
	dir, err := dirOf()
	if err != nil {
		res.Err = err
		return filePlan{res: res}
	}
	target := filepath.Join(dir, name)
	if holder, ok := o.contentAt(target); ok {
		identical, err := sameContent(src, holder)
		if err != nil {
			res.Dest, res.Err = target, fmt.Errorf("comparing %q: %w", target, err)
			return filePlan{res: res}
		}
		if identical {
			res.Dest, res.Action, res.Size = target, ActionSkippedIdentical, fileSize(holder)
			return filePlan{res: res}
		}
		// A previous run — or an earlier photo of this one — may already have placed
		// this exact content under a " (N)" name. Without this check every re-run
		// would re-collide on the original name and copy the same bytes again under
		// the next free suffix, so re-runs would not be idempotent (SC-002/SC-008).
		placed, placedHolder, err := o.existingIdentical(dir, name, src)
		if err != nil {
			res.Dest, res.Err = target, fmt.Errorf("comparing collision variants of %q: %w", target, err)
			return filePlan{res: res}
		}
		if placed != "" {
			res.Dest = filepath.Join(dir, placed)
			res.Action, res.Size = ActionSkippedIdentical, fileSize(placedHolder)
			return filePlan{res: res}
		}
		name = uniqueName(dir, name, o.taken)
		target = filepath.Join(dir, name)
		o.reserve(target, src)
		res.Dest, res.Action = target, ActionRenamed
		return filePlan{res: res, write: true}
	}
	o.reserve(target, src)
	res.Dest, res.Action = target, ActionCopied
	return filePlan{res: res, write: true}
}

// copy performs the placement, or merely reserves the destination name when this is
// a dry run. Routing every write through here is what makes "a dry run writes
// nothing" a property of one line rather than a rule each caller must remember — and
// the same now holds for "only a verified copy removes its source", since this is
// also the only place a source is ever deleted.
func (o *Organizer) copy(src, dst string) (int64, bool, error) {
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
