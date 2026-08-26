// Package ui renders moraine's stderr as bullet lines with progress bars, as an
// alternative to the plain slog text records. It is presentation and nothing else:
// the pipeline reports what it is doing as data (app.Progress, and slog records),
// and this package decides how that looks.
//
// Which of the two renderings a run gets is decided once, by Enabled, and the plain
// one stays reachable on demand — it is the form to read when debugging, since every
// line is self-contained, greppable and diffable.
package ui

import (
	"io"
	"log/slog"
	"os"

	"github.com/charmbracelet/x/term"

	"github.com/sgaunet/moraine/internal/config"
)

// Enabled reports whether the bullet renderer may draw for this run, given the
// requested mode and where the two streams point.
//
// The auto rule is deliberately conservative, and each clause answers a different
// question:
//
//   - Both stdout and stderr must be terminals. Principle V forbids progress bars
//     when stdout is not a TTY, and that is read literally: redirecting the run
//     result to a file turns the bars off.
//   - NO_COLOR must be unset. The bullets library colours unconditionally, with no
//     monochrome mode to fall back to, so honouring NO_COLOR means not drawing.
//   - TERM must not be "dumb". Such a terminal cannot move a cursor, and every
//     redraw depends on it.
//   - The verbosity must be the middle of the range. --verbose (slog.LevelDebug)
//     asks for a line per file, which fights a bar for the same rows and is the
//     output a debugging session wants anyway; --quiet (slog.LevelError) asks for
//     silence, and a bar is not silence.
func Enabled(mode config.ProgressMode, stdout, stderr io.Writer, level slog.Level) bool {
	switch mode {
	case config.ProgressNever:
		return false
	case config.ProgressAlways:
		return true
	case config.ProgressAuto:
	default:
		return false
	}
	if !terminalCheck(stdout) || !terminalCheck(stderr) {
		return false
	}
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	return level > slog.LevelDebug && level < slog.LevelError
}

// terminalCheck answers whether a stream is a terminal. It is a variable because a
// test process has no terminal to offer, and the decision table above is worth
// testing clause by clause rather than only in the one branch a pipe can reach.
var terminalCheck = IsTerminal

// IsTerminal reports whether v is a terminal. It takes any so one predicate serves
// both a writer being drawn to and a reader being prompted on: the question is only
// ever whether the value is an *os.File the driver recognises.
func IsTerminal(v any) bool {
	f, ok := v.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(f.Fd())
}
