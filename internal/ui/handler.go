package ui

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"github.com/charmbracelet/x/term"
	"github.com/sgaunet/bullets"
)

// barWidth is the width of the drawn progress bar, and reserve is how many columns a
// bar line needs beyond its label: the bar itself, the "[]", a space and " 100%".
const (
	barWidth = 20
	reserve  = barWidth + len("[] 100%") + 1
	// prefix is what bullets puts in front of every line at the padding this
	// renderer uses: the bullet, a space, and one indent level.
	prefix = 4
	// fallbackWidth is assumed when the terminal will not say how wide it is, which
	// happens when --progress=always is used off a terminal.
	fallbackWidth = 80
	// minLabel keeps a truncated label from collapsing to nothing on a narrow
	// terminal; below this the line is allowed to be wider than the screen, since a
	// label of two characters would be worse than a wrapped one.
	minLabel = 12
)

// Renderer draws a run on one writer as bullet lines and progress bars. It owns that
// writer completely for the duration: every redraw is relative to the cursor, so a
// second writer interleaving lines would desynchronise the display. Handler is the
// only way log records get in, and the Progress methods the only way bars do.
//
// A Renderer is safe for concurrent use.
type Renderer struct {
	log   *bullets.UpdatableLogger
	level slog.Level
	out   io.Writer
	// preview is set for a run that reports what it would do without doing it —
	// sort --dry-run, and clean or undo without --delete. It changes only the words
	// a phase wears, so a preview never claims to have written anything.
	preview bool

	mu sync.Mutex // serialises Begin against itself; bullets guards its own state
}

// New builds a Renderer drawing on w, showing records at level and above. Set
// preview for a run that only reports what it would do.
//
// The bullets logger is deliberately left at its most verbose: it bumps its internal
// line count even for a record its own level suppresses, which would offset every
// later in-place redraw. Gating happens in the slog handler instead, so a suppressed
// record never reaches it.
func New(w io.Writer, level slog.Level, preview bool) *Renderer {
	log := bullets.NewUpdatable(w)
	log.SetLevel(bullets.DebugLevel)
	log.SetProgressBarWidth(barWidth)
	// One indent level for the whole run, so every line reads as part of it.
	log.IncreasePadding()
	return &Renderer{log: log, level: level, out: w, preview: preview}
}

// Handler returns the slog.Handler that renders records as bullets.
func (r *Renderer) Handler() slog.Handler { return &handler{r: r} }

// width reports the terminal's current width, or fallbackWidth when it will not say.
// It is asked per line rather than cached: a mid-run resize would otherwise leave the
// renderer truncating to a width the terminal no longer has, and a line that wraps is
// exactly what breaks the cursor arithmetic every redraw depends on.
func (r *Renderer) width() int {
	f, ok := r.out.(interface{ Fd() uintptr })
	if !ok {
		return fallbackWidth
	}
	w, _, err := term.GetSize(f.Fd())
	if err != nil || w <= 0 {
		return fallbackWidth
	}
	return w
}

// truncate shortens s to fit cols columns, marking the cut with an ellipsis. It counts
// runes rather than bytes so a multi-byte path is not cut mid-character.
func truncate(s string, cols int) string {
	if cols < minLabel {
		return s
	}
	runes := []rune(s)
	if len(runes) <= cols {
		return s
	}
	return string(runes[:cols-1]) + "…"
}

// phaseNarration lists the log messages this rendering drops because a bar already
// says what they say. Membership has one criterion: the message arrives **once per
// unit of work** and adds nothing to the bar tracking those units. A per-run line is
// never a candidate, however much it overlaps a bar — it costs one row, and it is
// usually carrying a fact the bar has no room for.
//
// The pipeline emits these because the *text* rendering has nowhere else to put
// per-event facts: the stdout summary is one line per run by contract, so a run could
// otherwise report totals without ever saying which event produced them. Here the
// classify and copy bars say it continuously, and saying it again once per event means
// a large library pushes the bars steadily up the screen while telling a reader
// nothing new.
//
// Matching on the message is deliberately narrow, not a general filtering mechanism:
// these are moraine's own log calls in this one repository. The transport's suite
// drives a real run through the plain rendering and fails if the pipeline stops
// emitting one of them, so a rename cannot quietly turn this into a filter for
// nothing.
//
// Nothing is lost: --output=json carries every event in its events array, and
// --progress=never restores the lines themselves.
var phaseNarration = map[string]bool{
	// Organize logs one of these per event, immediately before placing it, naming the
	// theme it was given and how. Summary.Events carries the same facts as data.
	//
	// Its per-run neighbours stay: "exif" also reports raw=N, and "cluster" the gap
	// that produced the grouping, neither of which a bar shows.
	"group": true,
}

// PhaseNarration returns the log messages this rendering drops, sorted. It is
// exported for the transport's suite, which drives a real run through the *plain*
// rendering and asserts each one still appears there: the filter matches on the
// message, so a renamed message would otherwise leave it silently matching nothing.
func PhaseNarration() []string {
	out := make([]string, 0, len(phaseNarration))
	for msg := range phaseNarration {
		out = append(out, msg)
	}
	slices.Sort(out)
	return out
}

// handler renders slog records as bullet lines. WithAttrs and WithGroup return new
// handlers sharing the one Renderer, since they share the one terminal.
type handler struct {
	r      *Renderer
	attrs  []slog.Attr
	groups []string
}

// Enabled does all the level gating for this renderer, which is why the underlying
// bullets logger is left wide open. See New.
func (h *handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.r.level
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	out := &handler{r: h.r, groups: h.groups}
	out.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return out
}

func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	out := &handler{r: h.r, attrs: h.attrs}
	out.groups = append(append([]string{}, h.groups...), name)
	return out
}

// Handle renders one record: the message, then its attributes as key=value, the same
// pairs the text handler writes so the two renderings never disagree about a run.
// Neither the timestamp nor the level word is repeated — the bullet's colour carries
// the level, and a line appearing as it happens does not need to be dated.
func (h *handler) Handle(_ context.Context, rec slog.Record) error {
	// Dropped before anything is written, so the library's line counter does not
	// advance for a row that was never drawn — every later repaint is relative to it.
	if phaseNarration[rec.Message] {
		return nil
	}

	var b strings.Builder
	b.WriteString(rec.Message)
	for _, a := range h.attrs {
		appendAttr(&b, h.groups, a)
	}
	rec.Attrs(func(a slog.Attr) bool {
		appendAttr(&b, h.groups, a)
		return true
	})

	line := truncate(b.String(), h.r.width()-prefix)
	switch {
	case rec.Level >= slog.LevelError:
		h.r.log.Error(line)
	case rec.Level >= slog.LevelWarn:
		h.r.log.Warn(line)
	case rec.Level >= slog.LevelInfo:
		h.r.log.Info(line)
	default:
		h.r.log.Debug(line)
	}
	return nil
}

// appendAttr writes one attribute as " key=value", flattening a group into dotted
// keys. A value holding a space is quoted, so a message and its attributes cannot be
// read as one run-on phrase.
func appendAttr(b *strings.Builder, groups []string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	if a.Value.Kind() == slog.KindGroup {
		for _, sub := range a.Value.Group() {
			appendAttr(b, append(groups, a.Key), sub)
		}
		return
	}
	b.WriteByte(' ')
	for _, g := range groups {
		b.WriteString(g)
		b.WriteByte('.')
	}
	b.WriteString(a.Key)
	b.WriteByte('=')
	v := a.Value.String()
	if v == "" || strings.ContainsAny(v, " \t\"") {
		_, _ = fmt.Fprintf(b, "%q", v)
		return
	}
	b.WriteString(v)
}
