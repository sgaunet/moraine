package ui_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/sgaunet/moraine/internal/app"
	"github.com/sgaunet/moraine/internal/ui"
)

// A phase with no units must not divide by zero — the library's Progress does
// (current*100)/total with no guard, and an empty source directory reaches it.
func TestBeginWithNoUnitsIsInert(t *testing.T) {
	var buf bytes.Buffer
	r := ui.New(&buf, slog.LevelInfo, false)

	for _, phase := range []app.Phase{app.PhaseEXIF, app.PhaseCopy, app.PhaseIndex} {
		bar := r.Begin(phase, 0)
		bar.Inc() // would panic if the total reached the bar arithmetic
		bar.Close()
	}
	if strings.Contains(buf.String(), "%") {
		t.Errorf("an empty phase has no percentage to show:\n%s", buf.String())
	}
}

// Close is idempotent: clean closes its hashing phase at the first result and again on
// the way out, and an interrupted run closes phases with units outstanding.
func TestCloseIsIdempotent(t *testing.T) {
	var buf bytes.Buffer
	r := ui.New(&buf, slog.LevelInfo, false)

	bar := r.Begin(app.PhaseEXIF, 4)
	bar.Inc()
	bar.Close()
	bar.Close()
	bar.Inc() // after Close, nothing more is reported

	if n := strings.Count(strip(buf.String()), "metadata read"); n != 1 {
		t.Errorf("the closing line should appear exactly once, got %d:\n%s", n, buf.String())
	}
}

// The closing line spells out done-of-total only when the two differ, which is exactly
// when a run was interrupted or a stage stopped early.
func TestClosingLineReportsWhatWasReached(t *testing.T) {
	tests := []struct {
		name       string
		total, inc int
		preview    bool
		want       string
	}{
		{name: "complete", total: 3, inc: 3, want: "metadata read · 3"},
		{name: "interrupted", total: 9, inc: 4, want: "metadata read · 4 of 9"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			bar := ui.New(&buf, slog.LevelInfo, tc.preview).Begin(app.PhaseEXIF, tc.total)
			for range tc.inc {
				bar.Inc()
			}
			bar.Close()
			if got := strip(buf.String()); !strings.Contains(got, tc.want) {
				t.Errorf("want %q in:\n%s", tc.want, got)
			}
		})
	}
}

// A preview must not sign off with "photos placed" when nothing was written. clean and
// undo preview by default, so this is the wording most runs see.
func TestPreviewWordingNeverClaimsAWrite(t *testing.T) {
	tests := []struct {
		phase     app.Phase
		committed string
		previewed string
	}{
		{phase: app.PhaseCopy, committed: "photos placed", previewed: "photos checked"},
		{phase: app.PhaseUndo, committed: "copies removed", previewed: "copies checked"},
	}
	for _, tc := range tests {
		t.Run(string(tc.phase), func(t *testing.T) {
			for _, preview := range []bool{false, true} {
				var buf bytes.Buffer
				bar := ui.New(&buf, slog.LevelInfo, preview).Begin(tc.phase, 1)
				bar.Inc()
				bar.Close()

				got := strip(buf.String())
				want, unwanted := tc.committed, tc.previewed
				if preview {
					want, unwanted = tc.previewed, tc.committed
				}
				if !strings.Contains(got, want) {
					t.Errorf("preview=%v: want %q in:\n%s", preview, want, got)
				}
				if strings.Contains(got, unwanted) {
					t.Errorf("preview=%v: must not say %q in:\n%s", preview, unwanted, got)
				}
			}
		})
	}
}

// The phases that read rather than write mean the same thing either way, so they must
// not sprout a second vocabulary.
func TestReadOnlyPhasesShareOneWording(t *testing.T) {
	for _, phase := range []app.Phase{app.PhaseEXIF, app.PhaseClassify, app.PhaseIndex} {
		var committed, previewed bytes.Buffer
		for buf, preview := range map[*bytes.Buffer]bool{&committed: false, &previewed: true} {
			bar := ui.New(buf, slog.LevelInfo, preview).Begin(phase, 1)
			bar.Inc()
			bar.Close()
		}
		if strip(committed.String()) != strip(previewed.String()) {
			t.Errorf("%s should read the same either way:\n%s\n%s",
				phase, committed.String(), previewed.String())
		}
	}
}

// An unnamed phase still renders, under its own name, rather than as a blank line: a
// phase added to app without a label here must degrade rather than disappear.
func TestUnknownPhaseFallsBackToItsName(t *testing.T) {
	var buf bytes.Buffer
	bar := ui.New(&buf, slog.LevelInfo, false).Begin(app.Phase("verify"), 2)
	bar.Inc()
	bar.Close()
	if got := strip(buf.String()); !strings.Contains(got, "verify") {
		t.Errorf("want the phase name in:\n%s", got)
	}
}

// Inc is called from the EXIF pool, the look-ahead classifier and the copy pool, so it
// has to be safe under -race with the tally it feeds.
func TestBarIsSafeForConcurrentUse(t *testing.T) {
	var buf bytes.Buffer
	const n = 200
	bar := ui.New(&buf, slog.LevelInfo, false).Begin(app.PhaseCopy, n)

	done := make(chan struct{})
	for range n {
		go func() {
			bar.Inc()
			done <- struct{}{}
		}()
	}
	for range n {
		<-done
	}
	bar.Close()
	if got := strip(buf.String()); !strings.Contains(got, "photos placed · 200") {
		t.Errorf("every unit should have been counted:\n%s", got)
	}
}
