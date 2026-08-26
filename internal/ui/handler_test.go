package ui_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/sgaunet/moraine/internal/app"
	"github.com/sgaunet/moraine/internal/ui"
)

// strip removes the ANSI colouring, so a test asserts on words rather than escapes.
func strip(s string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, "\x1b[")
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		s = s[i+2:]
		if j := strings.IndexByte(s, 'm'); j >= 0 {
			s = s[j+1:]
		}
	}
}

// newTestRenderer draws into a buffer. A buffer is not an *os.File, so the library
// treats it as a non-terminal and appends plain lines instead of moving the cursor —
// which is exactly what makes the rendering assertable without a pty.
func newTestRenderer(t *testing.T, level slog.Level) (*ui.Renderer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	return ui.New(&buf, level, false), &buf
}

func TestHandlerRendersMessageAndAttrs(t *testing.T) {
	r, buf := newTestRenderer(t, slog.LevelInfo)
	slog.New(r.Handler()).Info("scan", "images", 3, "raw", 0)

	got := strip(buf.String())
	for _, want := range []string{"scan", "images=3", "raw=0"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// Neither the timestamp nor the level word is repeated: the bullet carries the
	// level, and a line drawn as it happens does not need to be dated.
	for _, unwanted := range []string{"time=", "level=INFO", "msg="} {
		if strings.Contains(got, unwanted) {
			t.Errorf("unexpected %q in:\n%s", unwanted, got)
		}
	}
}

// A value holding a space is quoted, so a message and its attributes cannot be read
// as one run-on phrase — the same choice the text handler makes.
func TestHandlerQuotesValuesWithSpaces(t *testing.T) {
	r, buf := newTestRenderer(t, slog.LevelInfo)
	slog.New(r.Handler()).Info("clean", "reason", "identical copy found")

	if got := strip(buf.String()); !strings.Contains(got, `reason="identical copy found"`) {
		t.Errorf("value with spaces should be quoted, got:\n%s", got)
	}
}

// Gating happens here and not in the library, whose line counter advances even for a
// record it suppresses — which would offset every later in-place redraw.
func TestHandlerSuppressesRecordsBelowItsLevel(t *testing.T) {
	r, buf := newTestRenderer(t, slog.LevelWarn)
	log := slog.New(r.Handler())
	log.Debug("per-file narration")
	log.Info("scan")
	if buf.Len() != 0 {
		t.Fatalf("debug and info must not reach a warn renderer, got:\n%s", buf.String())
	}
	log.Warn("destination may be too small")
	if !strings.Contains(strip(buf.String()), "destination may be too small") {
		t.Errorf("warn should have been drawn, got:\n%s", buf.String())
	}
}

// WithAttrs and WithGroup share the one terminal but keep their own attributes, and a
// group flattens to a dotted key.
func TestHandlerWithAttrsAndGroup(t *testing.T) {
	r, buf := newTestRenderer(t, slog.LevelInfo)
	log := slog.New(r.Handler()).With("run", "abc").WithGroup("space")
	log.Info("preflight", "needed_bytes", 12)

	got := strip(buf.String())
	for _, want := range []string{"preflight", "run=abc", "space.needed_bytes=12"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// Every level reaches the library, so no record is silently dropped on the way.
func TestHandlerRendersEveryLevel(t *testing.T) {
	r, buf := newTestRenderer(t, slog.LevelDebug)
	log := slog.New(r.Handler())
	log.Debug("d")
	log.Info("i")
	log.Warn("w")
	log.Error("e", "err", errors.New("boom"))

	got := strip(buf.String())
	if n := strings.Count(strings.TrimSpace(got), "\n"); n != 3 {
		t.Errorf("want 4 lines, got %d:\n%s", n+1, got)
	}
	if !strings.Contains(got, "err=boom") {
		t.Errorf("the error value should be rendered, got:\n%s", got)
	}
}

// A line long enough to wrap would desynchronise every later redraw, since each one is
// relative to the cursor. Off a terminal the width is unknown, so the fallback applies.
func TestHandlerTruncatesLongLines(t *testing.T) {
	r, buf := newTestRenderer(t, slog.LevelInfo)
	slog.New(r.Handler()).Info("file skipped", "file", strings.Repeat("x", 400))

	line := strings.TrimRight(strip(buf.String()), "\n")
	if len([]rune(line)) > 80 {
		t.Errorf("line is %d columns, wider than the assumed terminal:\n%s", len([]rune(line)), line)
	}
	if !strings.HasSuffix(line, "…") {
		t.Errorf("a truncated line should say so with an ellipsis, got:\n%s", line)
	}
}

// Handler.Enabled is what slog consults before building a record, so it has to agree
// with the level the renderer was given.
func TestHandlerEnabled(t *testing.T) {
	r, _ := newTestRenderer(t, slog.LevelInfo)
	h := r.Handler()
	if h.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("debug should be disabled at info")
	}
	if !h.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("info should be enabled at info")
	}
}

// A message a bar already accounts for is dropped: the classify and copy bars say it
// continuously, and it arrives once per event, so a large library would push the bars
// up the screen for nothing.
func TestHandlerDropsWhatABarAlreadySays(t *testing.T) {
	r, buf := newTestRenderer(t, slog.LevelInfo)
	log := slog.New(r.Handler())
	for _, msg := range ui.PhaseNarration() {
		log.Info(msg, "size", 23, "theme", "other")
	}
	if buf.Len() != 0 {
		t.Errorf("phase narration should not be drawn:\n%s", buf.String())
	}

	// Only those: everything else the pipeline says still reaches the screen.
	log.Info("scan", "images", 3)
	if got := strip(buf.String()); !strings.Contains(got, "scan") {
		t.Errorf("an ordinary record must still be drawn, got:\n%s", got)
	}
}

// Dropping must not advance the library's line counter, or every later repaint would
// be offset by a row that was never drawn. A bar opened after a dropped record and
// then closed is the observable check: its closing line replaces its own row.
func TestDroppedRecordDoesNotShiftLaterRows(t *testing.T) {
	r, buf := newTestRenderer(t, slog.LevelInfo)
	log := slog.New(r.Handler())
	log.Info("group", "size", 1)

	bar := r.Begin(app.PhaseCopy, 1)
	bar.Inc()
	bar.Close()

	got := strip(buf.String())
	if strings.Contains(got, "group") {
		t.Errorf("the dropped record leaked:\n%s", got)
	}
	if !strings.Contains(got, "photos placed · 1") {
		t.Errorf("the bar should still have closed on its own row:\n%s", got)
	}
}
