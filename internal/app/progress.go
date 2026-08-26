package app

// This file defines the pipeline's progress seam. It is the same shape as the
// onResult callback the orchestrators already take (architecture decision 8): the
// pipeline reports what it is doing as *data*, and the transport decides whether
// that becomes a progress bar, a log line, or nothing at all. Nothing here draws.

// Phase names a stage of a run whose advancement is worth reporting. A phase opened
// with a total of 0 is indeterminate — it has a beginning and an end but no
// countable units — which is a different thing from a phase with nothing to do.
type Phase string

// The reportable phases.
const (
	// PhaseEXIF is the metadata read, one unit per file the scan found.
	PhaseEXIF Phase = "exif"
	// PhaseClassify is theme assignment, one unit per event.
	PhaseClassify Phase = "classify"
	// PhaseCopy is placement, one unit per photo — a photo and its companion
	// files travel together, so the companions are not counted separately.
	PhaseCopy Phase = "copy"
	// PhaseUndo is the unwinding of a run manifest, one unit per record.
	PhaseUndo Phase = "undo"
	// PhaseIndex is clean's content-hash pass over the destination. It is always
	// indeterminate: the pass hashes what a walk finds as it finds it, so nothing
	// upstream knows how many files that will be until it is over.
	PhaseIndex Phase = "index"
)

// Progress receives the run's progress so a transport can render it.
//
// Implementations MUST be safe for concurrent use. This is the one seam in the
// pipeline that is not confined to Organize's own goroutine: the EXIF pool, the
// look-ahead classifier and the copy pool each report from their own. Everything
// else — Summary, manifest.Writer, onResult — stays single-goroutine by design, and
// keeping the two apart is why that is still true.
type Progress interface {
	// Begin opens a phase that will process total units and returns the tracker to
	// report them against. A total of 0 is legal: an empty run still opens and
	// closes its phases, so the renderer sees a consistent sequence.
	Begin(phase Phase, total int) Tracker
}

// Tracker counts one phase's progress. Inc and Close may be called from any
// goroutine; Close is idempotent.
type Tracker interface {
	// Inc reports one completed unit.
	Inc()
	// Close ends the phase, whether or not every unit was reached — an interrupted
	// run closes the phase it was in with units still outstanding.
	Close()
}

// noProgress is the nil-Progress substitute, so every call site can report
// unconditionally instead of guarding (the shape onResult already uses).
type noProgress struct{}

func (noProgress) Begin(Phase, int) Tracker { return noTracker{} }

type noTracker struct{}

func (noTracker) Inc()   {}
func (noTracker) Close() {}
