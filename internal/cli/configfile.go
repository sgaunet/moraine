package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sgaunet/moraine/internal/config"
	"github.com/sgaunet/moraine/internal/configfile"
)

// This file is the only place that knows about the three-layer precedence a
// configuration file introduces: a command-line flag beats the file, and the file
// beats the built-in default.
//
// "Was this flag typed?" is answered by cobra's Flags().Changed, not by comparing a
// value against its default — a user who types --sample 3 when 3 is already the
// default still means it, and a file must not override them. That also keeps every
// flag default defined in exactly one place (the pflag registration) instead of
// growing a second copy here.

// overlayer applies file values to one command's options, recording which settings it
// took from the file so a later validation error can say where they came from.
type overlayer struct {
	cmd  *cobra.Command
	used []string
}

// set applies a file value, unless the setting is absent from the file or the user
// typed the flag. name is the flag's name, which is also what the error hint reports.
func (o *overlayer) set(name string, present bool, apply func()) {
	if !present || o.cmd.Flags().Changed(name) {
		return
	}
	apply()
	o.used = append(o.used, name)
}

// applySortFile overlays the file's sort settings onto opts.
func applySortFile(cmd *cobra.Command, opts *config.Options, f *configfile.File) []string {
	s := f.SortSection()
	o := &overlayer{cmd: cmd}
	o.set("dest", s.Dest != nil, func() { opts.Dest = *s.Dest })
	o.set("log-level", s.LogLevel != nil, func() { opts.LogLevel = *s.LogLevel })
	o.set("output", s.Output != nil, func() { opts.Output = *s.Output })
	o.set("progress", s.Progress != nil, func() { opts.Progress = *s.Progress })
	o.set("model", s.Model != nil, func() { opts.Model = *s.Model })
	o.set("gap", s.Gap != nil, func() { opts.Gap = s.Gap.Duration })
	o.set("sample", s.Sample != nil, func() { opts.Sample = *s.Sample })
	o.set("ollama-url", s.OllamaURL != nil, func() { opts.OllamaURL = *s.OllamaURL })
	// A YAML list reads better than an embedded comma string; config.ParseThemes
	// still owns what a valid theme is.
	o.set("themes", s.Themes != nil, func() { opts.Themes = strings.Join(s.Themes, ",") })
	o.set("fallback-theme", s.FallbackTheme != nil, func() { opts.Fallback = *s.FallbackTheme })
	o.set("path-template", s.PathTemplate != nil, func() { opts.PathTemplate = *s.PathTemplate })
	o.set("exiftool", s.ExifTool != nil, func() { opts.ExifTool = *s.ExifTool })
	o.set("sidecars", s.Sidecars != nil, func() { opts.Sidecars = *s.Sidecars })
	o.set("jobs", s.Jobs != nil, func() { opts.Jobs = *s.Jobs })
	o.set("mountain-altitude", s.MountainAltitude != nil, func() { opts.MountainAltitude = *s.MountainAltitude })
	o.set("min-confidence", s.MinConfidence != nil, func() { opts.MinConfidence = *s.MinConfidence })
	o.set("vote", s.Vote != nil, func() { opts.Vote = *s.Vote })
	return o.used
}

// applyCleanFile overlays the file's clean settings onto opts.
func applyCleanFile(cmd *cobra.Command, opts *config.CleanOptions, f *configfile.File) []string {
	c := f.CleanSection()
	o := &overlayer{cmd: cmd}
	o.set("dest", c.Dest != nil, func() { opts.Dest = *c.Dest })
	o.set("log-level", c.LogLevel != nil, func() { opts.LogLevel = *c.LogLevel })
	o.set("output", c.Output != nil, func() { opts.Output = *c.Output })
	o.set("progress", c.Progress != nil, func() { opts.Progress = *c.Progress })
	return o.used
}

// applyUndoFile overlays the file's undo settings onto opts. undo takes its
// destination as an argument, so there is no dest to configure.
func applyUndoFile(cmd *cobra.Command, opts *config.UndoOptions, f *configfile.File) []string {
	u := f.UndoSection()
	o := &overlayer{cmd: cmd}
	o.set("log-level", u.LogLevel != nil, func() { opts.LogLevel = *u.LogLevel })
	o.set("output", u.Output != nil, func() { opts.Output = *u.Output })
	o.set("progress", u.Progress != nil, func() { opts.Progress = *u.Progress })
	return o.used
}

// fileHint annotates a config-construction error with the file that supplied some of
// its inputs. Without it a bad value in a configuration file produces a message
// naming a flag the user never typed ("--gap must be strictly positive"), which sends
// them looking in the wrong place.
func fileHint(err error, path string, used []string) error {
	if err == nil || path == "" || len(used) == 0 {
		return err
	}
	return fmt.Errorf("%w (settings read from %s: %s)", err, path, strings.Join(used, ", "))
}
