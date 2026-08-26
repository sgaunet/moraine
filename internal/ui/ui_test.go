package ui_test

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/sgaunet/moraine/internal/config"
	"github.com/sgaunet/moraine/internal/ui"
)

// yes and no stand in for the terminal predicate, so each clause of the auto rule can
// be exercised on its own rather than only through the one a pipe reaches.
func yes(any) bool { return true }
func no(any) bool  { return false }

// The explicit modes answer without asking anything about the terminal: --progress is
// how a user overrides the decision, so it has to beat every clause of it.
func TestEnabledExplicitModesIgnoreEverythingElse(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "dumb")
	restore := ui.SetTerminalCheck(no)
	defer restore()

	var buf bytes.Buffer
	if !ui.Enabled(config.ProgressAlways, &buf, &buf, slog.LevelDebug) {
		t.Error("always must draw even with no terminal, NO_COLOR, TERM=dumb and debug")
	}
	restore()
	_ = ui.SetTerminalCheck(yes)
	if ui.Enabled(config.ProgressNever, &buf, &buf, slog.LevelInfo) {
		t.Error("never must not draw on a perfectly good terminal")
	}
}

// An unknown mode is refused rather than guessed at. config.ParseProgress rejects one
// on the way in, so this is the belt to that braces.
func TestEnabledRefusesAnUnknownMode(t *testing.T) {
	defer ui.SetTerminalCheck(yes)()
	var buf bytes.Buffer
	if ui.Enabled(config.ProgressMode("sometimes"), &buf, &buf, slog.LevelInfo) {
		t.Error("an unrecognised mode must not draw")
	}
}

func TestEnabledAuto(t *testing.T) {
	tests := []struct {
		name      string
		terminal  func(any) bool
		noColor   string
		term      string
		level     slog.Level
		wantDrawn bool
	}{
		{name: "terminal, info", terminal: yes, term: "xterm-256color", level: slog.LevelInfo, wantDrawn: true},
		{name: "terminal, warn", terminal: yes, term: "xterm-256color", level: slog.LevelWarn, wantDrawn: true},
		// Principle V, read literally: progress bars need stdout to be a terminal too,
		// so redirecting the run result turns them off.
		{name: "not a terminal", terminal: no, term: "xterm-256color", level: slog.LevelInfo},
		// The library colours unconditionally, so honouring NO_COLOR means not drawing.
		{name: "NO_COLOR set", terminal: yes, noColor: "1", term: "xterm-256color", level: slog.LevelInfo},
		{name: "TERM=dumb", terminal: yes, term: "dumb", level: slog.LevelInfo},
		// --verbose wants a line per file, which is the output that fights a bar for
		// the same rows; --quiet wants silence, and a bar is not silence.
		{name: "debug (--verbose)", terminal: yes, term: "xterm-256color", level: slog.LevelDebug},
		{name: "error (--quiet)", terminal: yes, term: "xterm-256color", level: slog.LevelError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", tc.noColor)
			t.Setenv("TERM", tc.term)
			defer ui.SetTerminalCheck(tc.terminal)()

			var buf bytes.Buffer
			if got := ui.Enabled(config.ProgressAuto, &buf, &buf, tc.level); got != tc.wantDrawn {
				t.Errorf("Enabled = %v, want %v", got, tc.wantDrawn)
			}
		})
	}
}

// A bytes.Buffer is not an *os.File, so IsTerminal must say no rather than panic on
// the type assertion — which is also what keeps the whole suite on the plain path.
func TestIsTerminalRefusesANonFile(t *testing.T) {
	if ui.IsTerminal(&bytes.Buffer{}) {
		t.Error("a buffer is not a terminal")
	}
	if ui.IsTerminal(nil) {
		t.Error("nil is not a terminal")
	}
}
