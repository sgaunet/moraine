package app_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sgaunet/moraine/internal/app"
)

// fakeProgress records what the pipeline reported, so a test can assert the stages
// counted every unit they took on. It locks because the real seam is fed from the EXIF
// pool, the look-ahead classifier and the copy pool at once — which is the property
// -race is here to check.
type fakeProgress struct {
	mu     sync.Mutex
	phases []*fakePhase
}

type fakePhase struct {
	name   app.Phase
	total  int
	ticks  int
	closed int
}

func (f *fakeProgress) Begin(phase app.Phase, total int) app.Tracker {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := &fakePhase{name: phase, total: total}
	f.phases = append(f.phases, p)
	return &fakeTracker{f: f, p: p}
}

// phase returns the single phase of a name, failing the test when the run opened it a
// different number of times than once.
func (f *fakeProgress) phase(t *testing.T, name app.Phase) *fakePhase {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var found []*fakePhase
	for _, p := range f.phases {
		if p.name == name {
			found = append(found, p)
		}
	}
	if len(found) != 1 {
		t.Fatalf("phase %q was opened %d times, want once", name, len(found))
	}
	return found[0]
}

type fakeTracker struct {
	f *fakeProgress
	p *fakePhase
}

func (t *fakeTracker) Inc() {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	t.p.ticks++
}

func (t *fakeTracker) Close() {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	t.p.closed++
}

// check asserts a phase's total, its tick count, and that it was closed at all — an
// unclosed phase leaves a bar stuck on screen for the rest of the run.
func (p *fakePhase) check(t *testing.T, total, ticks int) {
	t.Helper()
	if p.total != total {
		t.Errorf("%s total = %d, want %d", p.name, p.total, total)
	}
	if p.ticks != ticks {
		t.Errorf("%s ticks = %d, want %d", p.name, p.ticks, ticks)
	}
	if p.closed == 0 {
		t.Errorf("%s was never closed", p.name)
	}
}

// Every stage reports a total that matches what it was given, and one tick per unit:
// a file for the EXIF read, an event for classification, a photo for placement.
func TestOrganizeReportsEveryStage(t *testing.T) {
	src, dest := t.TempDir(), t.TempDir()
	for _, name := range []string{"a.png", "b.png", "c.png"} {
		makePNG(t, filepath.Join(src, name))
	}

	var prog fakeProgress
	sum, err := app.Organize(context.Background(), baseCfg(src, dest, true), quietLogger(), nil, &prog)
	if err != nil {
		t.Fatalf("Organize: %v", err)
	}

	// The photos share a modification time, so they cluster into one event.
	prog.phase(t, app.PhaseEXIF).check(t, 3, 3)
	prog.phase(t, app.PhaseClassify).check(t, sum.Groups, sum.Groups)
	prog.phase(t, app.PhaseCopy).check(t, 3, 3)
}

// A file the EXIF stage could not read is still a unit of work: the bar measures what
// the stage got through, not what it succeeded at, or it would never reach the end.
func TestEXIFPhaseCountsUnreadableFiles(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are ignored")
	}
	src, dest := t.TempDir(), t.TempDir()
	makePNG(t, filepath.Join(src, "good.png"))
	// Unreadable rather than malformed: the dating tiers keep a photo whose metadata
	// will not parse, so only a file that cannot be opened at all is counted.
	bad := filepath.Join(src, "bad.jpg")
	if err := os.WriteFile(bad, []byte("x"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0o644) })

	var prog fakeProgress
	sum, err := app.Organize(context.Background(), baseCfg(src, dest, true), quietLogger(), nil, &prog)
	if err != nil {
		t.Fatalf("Organize: %v", err)
	}
	if sum.Unreadable != 1 {
		t.Fatalf("Unreadable = %d, want 1 (the fixture is meant to be unopenable)", sum.Unreadable)
	}
	prog.phase(t, app.PhaseEXIF).check(t, 2, 2)
	// Only the readable photo reaches placement.
	prog.phase(t, app.PhaseCopy).check(t, 1, 1)
}

// A re-run places nothing new, and every unit is idle. The copy phase must still reach
// its total: the ticks come from the planning loop, not only from the copy workers.
func TestCopyPhaseAdvancesWhenEverythingIsSkipped(t *testing.T) {
	src, dest := t.TempDir(), t.TempDir()
	for _, name := range []string{"a.png", "b.png"} {
		makePNG(t, filepath.Join(src, name))
	}
	cfg := baseCfg(src, dest, true)
	if _, err := app.Organize(context.Background(), cfg, quietLogger(), nil, nil); err != nil {
		t.Fatalf("first run: %v", err)
	}

	var prog fakeProgress
	sum, err := app.Organize(context.Background(), cfg, quietLogger(), nil, &prog)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if sum.Copied != 0 || sum.Skipped != 2 {
		t.Fatalf("want the second run to skip both, got copied=%d skipped=%d", sum.Copied, sum.Skipped)
	}
	prog.phase(t, app.PhaseCopy).check(t, 2, 2)
}

// A dry run writes nothing, so every unit is idle for a different reason. The phases
// must still be reported, or a preview would draw no progress at all.
func TestDryRunStillReportsProgress(t *testing.T) {
	src, dest := t.TempDir(), t.TempDir()
	makePNG(t, filepath.Join(src, "a.png"))
	cfg := baseCfg(src, dest, true)
	cfg.DryRun = true

	var prog fakeProgress
	if _, err := app.Organize(context.Background(), cfg, quietLogger(), nil, &prog); err != nil {
		t.Fatalf("Organize: %v", err)
	}
	prog.phase(t, app.PhaseEXIF).check(t, 1, 1)
	prog.phase(t, app.PhaseCopy).check(t, 1, 1)
}

// An empty source still opens and closes its phases, so the renderer sees a consistent
// sequence — and a zero total is what reaches the bar arithmetic that cannot divide by
// it.
func TestEmptySourceStillOpensAndClosesPhases(t *testing.T) {
	src, dest := t.TempDir(), t.TempDir()

	var prog fakeProgress
	if _, err := app.Organize(context.Background(), baseCfg(src, dest, true), quietLogger(), nil, &prog); err != nil {
		t.Fatalf("Organize: %v", err)
	}
	prog.phase(t, app.PhaseEXIF).check(t, 0, 0)
	prog.phase(t, app.PhaseClassify).check(t, 0, 0)
	prog.phase(t, app.PhaseCopy).check(t, 0, 0)
}

// An interrupted run closes the phase it was in, with its units still outstanding: a
// bar left open would sit on screen forever at whatever it had reached.
//
// The cancellation is timed on the "scan" record — logged once the walk is done and
// before the first EXIF read — so the EXIF phase is opened and then abandoned, which
// is the case worth pinning. Timing it on a record rather than on a clock is what
// makes the test deterministic.
func TestInterruptedRunClosesThePhaseItWasIn(t *testing.T) {
	src, dest := t.TempDir(), t.TempDir()
	for _, name := range []string{"a.png", "b.png", "c.png"} {
		makePNG(t, filepath.Join(src, name))
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// A real handler, not slog.DiscardHandler: cancelOnMessage delegates Enabled, and a
	// discarding handler answers false to everything, so Handle — and the cancellation
	// with it — would never be reached.
	inner := slog.NewTextHandler(&safeBuffer{}, &slog.HandlerOptions{Level: slog.LevelInfo})
	logger := slog.New(&cancelOnMessage{msg: "scan", cancel: cancel, inner: inner})

	var prog fakeProgress
	if _, err := app.Organize(ctx, baseCfg(src, dest, true), logger, nil, &prog); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	// Opened with everything still to do, ticked never, and closed all the same.
	prog.phase(t, app.PhaseEXIF).check(t, 3, 0)

	prog.mu.Lock()
	defer prog.mu.Unlock()
	for _, p := range prog.phases {
		if p.closed == 0 {
			t.Errorf("phase %q was left open by the interrupt", p.name)
		}
	}
}

// An interrupted run must not report a full bar. Every photo the cancellation beat is
// recorded as never attempted, and a progress report is no more entitled than the
// tally to count those — otherwise a run that copied 30 of 400 signs off at 400.
//
// Timed on the "group" record, which Organize logs immediately before it places that
// event, so placement is entered with the context already cancelled: deterministic,
// where a sleep would be a race.
func TestInterruptedCopyPhaseDoesNotReportWhatItNeverPlaced(t *testing.T) {
	src, dest := t.TempDir(), t.TempDir()
	for _, name := range []string{"a.png", "b.png", "c.png"} {
		makePNG(t, filepath.Join(src, name))
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inner := slog.NewTextHandler(&safeBuffer{}, &slog.HandlerOptions{Level: slog.LevelInfo})
	logger := slog.New(&cancelOnMessage{msg: "group", cancel: cancel, inner: inner})

	var prog fakeProgress
	sum, err := app.Organize(ctx, baseCfg(src, dest, true), logger, nil, &prog)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if sum.Copied != 0 {
		t.Fatalf("Copied = %d, want 0: the cancellation landed before any placement", sum.Copied)
	}
	// Three photos were expected and none was reached, so the bar must say so rather
	// than close at 3 of 3.
	prog.phase(t, app.PhaseCopy).check(t, 3, 0)
}

// A nil Progress is the ordinary case — it is what the plain text rendering passes —
// so every call site has to tolerate it.
func TestNilProgressIsAccepted(t *testing.T) {
	src, dest := t.TempDir(), t.TempDir()
	makePNG(t, filepath.Join(src, "a.png"))
	if _, err := app.Organize(context.Background(), baseCfg(src, dest, true), quietLogger(), nil, nil); err != nil {
		t.Fatalf("Organize with a nil Progress: %v", err)
	}
}
