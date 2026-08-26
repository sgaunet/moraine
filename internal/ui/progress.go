package ui

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/sgaunet/bullets"

	"github.com/sgaunet/moraine/internal/app"
)

// redrawInterval bounds how often a bar repaints. A library of ten thousand photos
// would otherwise drive ten thousand cursor round-trips for an animation nobody can
// read that fast; the final unit always draws, so the bar still lands on 100%.
const redrawInterval = 75 * time.Millisecond

// label is what a phase is called while it runs and once it is done, in each of the
// two moods a run can be in. The pipeline names its phases; naming them for a reader
// is this package's job.
//
// The preview wording is not decoration. A dry run — which is what `clean` and `undo`
// do unless told otherwise — must not report "removed 412" for files that are all
// still there; the two stages that would write are the two that need the distinction.
type label struct {
	running, done               string
	previewRunning, previewDone string
}

// words returns the pair to use, falling back to the committing wording for the
// phases that read rather than write and so mean the same thing either way.
func (l label) words(preview bool) (running, done string) {
	if preview && l.previewRunning != "" {
		return l.previewRunning, l.previewDone
	}
	return l.running, l.done
}

var phaseLabel = map[app.Phase]label{
	app.PhaseEXIF:     {running: "reading metadata", done: "metadata read"},
	app.PhaseClassify: {running: "classifying events", done: "events classified"},
	app.PhaseCopy: {
		running: "copying photos", done: "photos placed",
		previewRunning: "checking photos", previewDone: "photos checked",
	},
	app.PhaseUndo: {
		running: "removing copies", done: "copies removed",
		previewRunning: "checking copies", previewDone: "copies checked",
	},
	app.PhaseIndex: {running: "hashing destination", done: "destination hashed"},
}

// Begin opens a phase and returns the tracker that draws it. A phase with a known
// total gets a progress bar; an indeterminate one gets a spinner, which is safe only
// because the single indeterminate phase — clean's hashing pass — logs nothing while
// it runs (see app.Clean).
func (r *Renderer) Begin(phase app.Phase, total int) app.Tracker {
	r.mu.Lock()
	defer r.mu.Unlock()

	run, done := phaseLabel[phase].words(r.preview)
	if run == "" {
		run, done = string(phase), string(phase)
	}
	running := truncate(run, r.width()-prefix-reserve)

	b := &Bar{r: r, total: total, label: done}
	if total > 0 {
		b.handle = r.log.InfoHandle(running)
		return b
	}
	// An empty phase has nothing to animate and nothing to wait for, so it is only
	// ever a spinner when the total is genuinely unknown rather than zero.
	if total == 0 && phase == app.PhaseIndex {
		b.spinner = r.log.Spinner(context.Background(), running)
		return b
	}
	b.handle = r.log.InfoHandle(running)
	return b
}

// Bar reports one phase's progress on one terminal row: a progress bar, or a spinner
// when the total is not knowable. It satisfies app.Tracker and is safe for concurrent
// use — the copy pool and the EXIF pool both report from their workers.
type Bar struct {
	r       *Renderer
	handle  *bullets.BulletHandle
	spinner *bullets.Spinner
	total   int
	label   string

	mu     sync.Mutex
	done   int
	last   time.Time
	closed bool
}

// Inc reports one completed unit, repainting at most every redrawInterval.
func (b *Bar) Inc() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.done++
	// A zero total is the library's divide-by-zero, and a spinner has no bar to fill.
	if b.total <= 0 || b.handle == nil {
		return
	}
	now := time.Now()
	if b.done < b.total && now.Sub(b.last) < redrawInterval {
		return
	}
	b.last = now
	b.handle.Progress(b.done, b.total)
}

// Close ends the phase, replacing the bar with what it achieved. It is idempotent:
// clean closes its hashing phase at the first result and again on the way out, and an
// interrupted run closes phases that still had units outstanding.
func (b *Bar) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	msg := truncate(b.summary(), b.r.width()-prefix)
	if b.spinner != nil {
		b.spinner.Success(msg)
		return
	}
	b.handle.Success(msg)
}

// summary is the line a finished phase leaves behind. It reports the count reached
// out of the count expected only when the two differ, which is exactly when the run
// was interrupted or a stage stopped early — the case worth spelling out.
func (b *Bar) summary() string {
	switch {
	case b.total <= 0:
		if b.done == 0 {
			return b.label
		}
		return b.label + " · " + strconv.Itoa(b.done)
	case b.done >= b.total:
		return b.label + " · " + strconv.Itoa(b.total)
	default:
		return b.label + " · " + strconv.Itoa(b.done) + " of " + strconv.Itoa(b.total)
	}
}
