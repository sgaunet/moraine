package cli

import (
	"io"
	"log/slog"

	"github.com/sgaunet/moraine/internal/app"
	"github.com/sgaunet/moraine/internal/config"
	"github.com/sgaunet/moraine/internal/ui"
)

// newRenderer builds a run's stderr rendering, and the progress sink that goes with
// it. There are exactly two renderings and this is the only place that chooses:
//
//   - the bullet renderer, which draws nested lines and a progress bar per phase, and
//     is also the app.Progress the pipeline reports into;
//   - the plain slog text handler, which is what moraine has always written and what
//     to reach for when debugging — one self-contained line per event, greppable,
//     diffable and safe to redirect.
//
// The plain rendering returns a nil app.Progress rather than a silent one: the
// orchestrators already substitute a no-op for nil, so nothing has to branch twice.
//
// preview says this run only reports what it would do — sort --dry-run, or clean and
// undo without --delete, which is their default. It reaches the renderer because a
// preview must not sign off a phase with "photos placed" when nothing was written;
// the plain rendering needs no equivalent, since its records state their own mode.
//
// Only stderr is ever drawn on. stdout carries the run result and nothing else
// (Constitution Principle V), which is why it is passed here to be inspected and
// never to be written to.
func newRenderer(
	mode config.ProgressMode, stdout, stderr io.Writer, level slog.Level, preview bool,
) (*slog.Logger, app.Progress) {
	if !ui.Enabled(mode, stdout, stderr, level) {
		return slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level})), nil
	}
	r := ui.New(stderr, level, preview)
	return slog.New(r.Handler()), r
}
