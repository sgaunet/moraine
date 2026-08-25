package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"runtime/debug"

	"github.com/sgaunet/moraine/internal/app"
	"github.com/sgaunet/moraine/internal/clean"
	"github.com/sgaunet/moraine/internal/config"
	"github.com/sgaunet/moraine/internal/organize"
	"github.com/sgaunet/moraine/internal/undo"
)

// This file is moraine's stdout contract (Constitution Principle V): stdout carries
// the run's result and nothing else, rendered either as a single key=value line
// (--output=text) or as one JSON object holding every per-file record plus the
// summary (--output=json). Logs, progress and errors go to stderr.
//
// The types below are therefore a public API, not an implementation detail. They are
// spelled out here rather than reusing app.Summary/clean.Summary directly so that
// renaming an internal tally field cannot silently change what scripts parse. The
// key names are asserted against the text renderer in the tests, so the two
// renderings cannot drift apart.

// dateFormat is how a record's representative date appears on stdout.
const dateFormat = "2006-01-02"

// sortReport is the stdout document of a `sort` run.
type sortReport struct {
	Command     string       `json:"command"`
	Source      string       `json:"source"`
	Dest        string       `json:"dest"`
	DryRun      bool         `json:"dry_run"`
	Interrupted bool         `json:"interrupted"`
	Results     []sortRecord `json:"results"`
	Events      []sortEvent  `json:"events"`
	Summary     sortSummary  `json:"summary"`
}

// sortEvent is one placed event. It exists only in the JSON rendering: the text
// rendering is one line per run by contract, so it can carry totals but not a
// per-event breakdown.
type sortEvent struct {
	Theme        string `json:"theme,omitempty"`
	Method       string `json:"method,omitempty"`
	Photos       int    `json:"photos"`
	Start        string `json:"start,omitempty"`
	End          string `json:"end,omitempty"`
	Copied       int    `json:"copied"`
	Skipped      int    `json:"skipped"`
	Renamed      int    `json:"renamed"`
	Errors       int    `json:"errors"`
	BytesCopied  int64  `json:"bytes_copied"`
	BytesSkipped int64  `json:"bytes_skipped"`
}

// sortRecord is one placed (or would-be-placed) file: a photo, or one of its
// companions when Companion is set.
type sortRecord struct {
	Source    string `json:"source"`
	Dest      string `json:"dest,omitempty"`
	Theme     string `json:"theme,omitempty"`
	Date      string `json:"date,omitempty"`
	Action    string `json:"action,omitempty"`
	Companion bool   `json:"companion"`
	Moved     bool   `json:"moved,omitempty"`
	Of        string `json:"of,omitempty"`
	Error     string `json:"error,omitempty"`
}

// sortSummary is the tally of a `sort` run.
type sortSummary struct {
	Scanned                int   `json:"scanned"`
	Unreadable             int   `json:"unreadable"`
	Groups                 int   `json:"groups"`
	Copied                 int   `json:"copied"`
	Skipped                int   `json:"skipped"`
	Renamed                int   `json:"renamed"`
	Errors                 int   `json:"errors"`
	Moved                  int   `json:"moved"`
	BytesCopied            int64 `json:"bytes_copied"`
	BytesSkipped           int64 `json:"bytes_skipped"`
	CompanionsCopied       int   `json:"companions_copied"`
	CompanionsSkipped      int   `json:"companions_skipped"`
	CompanionsRenamed      int   `json:"companions_renamed"`
	CompanionsErrors       int   `json:"companions_errors"`
	CompanionsBytesCopied  int64 `json:"companions_bytes_copied"`
	CompanionsBytesSkipped int64 `json:"companions_bytes_skipped"`
}

// cleanReport is the stdout document of a `clean` run.
type cleanReport struct {
	Command     string        `json:"command"`
	Source      string        `json:"source"`
	Dest        string        `json:"dest"`
	Delete      bool          `json:"delete"`
	Interrupted bool          `json:"interrupted"`
	Results     []cleanRecord `json:"results"`
	Summary     cleanSummary  `json:"summary"`
}

// cleanRecord is one evaluated source file and what clean decided about it.
type cleanRecord struct {
	Path     string `json:"path"`
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
	Error    string `json:"error,omitempty"`
}

// cleanSummary is the tally of a `clean` run.
type cleanSummary struct {
	Deleted      int `json:"deleted"`
	WouldDelete  int `json:"would_delete"`
	Kept         int `json:"kept"`
	Errors       int `json:"errors"`
	SourceHashed int `json:"source_hashed"`
	DestHashed   int `json:"dest_hashed"`
}

// undoReport is the stdout document of an `undo` run.
type undoReport struct {
	Command     string       `json:"command"`
	Dest        string       `json:"dest"`
	Delete      bool         `json:"delete"`
	Interrupted bool         `json:"interrupted"`
	Results     []undoRecord `json:"results"`
	Summary     undoSummary  `json:"summary"`
}

// undoRecord is one recorded copy and what undo decided about it.
type undoRecord struct {
	Path     string `json:"path"`
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
	Error    string `json:"error,omitempty"`
}

// undoSummary is the tally of an `undo` run.
type undoSummary struct {
	Removed     int `json:"removed"`
	WouldRemove int `json:"would_remove"`
	Kept        int `json:"kept"`
	Errors      int `json:"errors"`
	DirsPruned  int `json:"dirs_pruned"`
}

// reporter collects a run's per-file records and renders the result on stdout. It
// keeps records only in JSON mode: text mode prints one summary line, so holding
// every record of a 50k-photo library would be pure waste.
type reporter struct {
	format config.OutputFormat
	stdout io.Writer
	sort   []sortRecord
	events []sortEvent
	clean  []cleanRecord
	undo   []undoRecord
}

func newReporter(format config.OutputFormat, stdout io.Writer) *reporter {
	r := &reporter{format: format, stdout: stdout}
	if r.json() {
		// Start non-nil: an empty run must marshal "results":[] and not "results":null,
		// which would break a consumer that iterates the array.
		r.sort, r.clean, r.undo = []sortRecord{}, []cleanRecord{}, []undoRecord{}
		r.events = []sortEvent{}
	}
	return r
}

// json reports whether records need to be kept for the final document.
func (r *reporter) json() bool { return r.format == config.OutputJSON }

// addSort records one placement outcome.
func (r *reporter) addSort(res organize.Result) {
	if !r.json() {
		return
	}
	rec := sortRecord{
		Source:    res.Source,
		Dest:      res.Dest,
		Theme:     res.Theme,
		Action:    string(res.Action),
		Companion: res.IsCompanion,
		Moved:     res.Moved,
		Of:        res.Of,
	}
	if !res.Date.IsZero() {
		rec.Date = res.Date.Format(dateFormat)
	}
	if res.Err != nil {
		rec.Error = res.Err.Error()
	}
	r.sort = append(r.sort, rec)
}

// eventsOf converts the run's events into their wire form. Unlike the per-file
// records, events are not streamed through a callback: they arrive whole on the
// Summary, because there are only as many of them as there are events.
func (r *reporter) eventsOf(sum app.Summary) []sortEvent {
	out := r.events
	for _, e := range sum.Events {
		ev := sortEvent{
			Theme: e.Theme, Method: e.Method, Photos: e.Photos,
			Copied: e.Copied, Skipped: e.Skipped, Renamed: e.Renamed, Errors: e.Errors,
			BytesCopied: e.BytesCopied, BytesSkipped: e.BytesSkipped,
		}
		if !e.Start.IsZero() {
			ev.Start = e.Start.Format(dateFormat)
		}
		if !e.End.IsZero() {
			ev.End = e.End.Format(dateFormat)
		}
		out = append(out, ev)
	}
	return out
}

// addClean records one clean decision.
func (r *reporter) addClean(res clean.Result) {
	if !r.json() {
		return
	}
	rec := cleanRecord{Path: res.Path, Decision: string(res.Decision), Reason: res.Reason}
	if res.Err != nil {
		rec.Error = res.Err.Error()
	}
	r.clean = append(r.clean, rec)
}

// addUndo records one undo decision.
func (r *reporter) addUndo(res undo.Result) {
	if !r.json() {
		return
	}
	rec := undoRecord{Path: res.Path, Decision: string(res.Decision), Reason: res.Reason}
	if res.Err != nil {
		rec.Error = res.Err.Error()
	}
	r.undo = append(r.undo, rec)
}

// emitSort writes the result of a `sort` run to stdout.
func (r *reporter) emitSort(cfg config.Config, sum app.Summary, interrupted bool) error {
	s := sortSummary{
		Scanned:                sum.Scanned,
		Unreadable:             sum.Unreadable,
		Groups:                 sum.Groups,
		Copied:                 sum.Copied,
		Skipped:                sum.Skipped,
		Renamed:                sum.Renamed,
		Errors:                 sum.Errors,
		Moved:                  sum.Moved,
		BytesCopied:            sum.BytesCopied,
		BytesSkipped:           sum.BytesSkipped,
		CompanionsCopied:       sum.CompanionsCopied,
		CompanionsSkipped:      sum.CompanionsSkipped,
		CompanionsRenamed:      sum.CompanionsRenamed,
		CompanionsErrors:       sum.CompanionsErrors,
		CompanionsBytesCopied:  sum.CompanionsBytesCopied,
		CompanionsBytesSkipped: sum.CompanionsBytesSkipped,
	}
	if r.json() {
		return r.writeJSON(sortReport{
			Command: "sort", Source: cfg.Source, Dest: cfg.DestRoot,
			DryRun: cfg.DryRun, Interrupted: interrupted,
			Results: r.sort, Events: r.eventsOf(sum), Summary: s,
		})
	}
	_, err := fmt.Fprintf(r.stdout,
		"scanned=%d unreadable=%d groups=%d copied=%d skipped=%d renamed=%d errors=%d"+
			" moved=%d bytes_copied=%d bytes_skipped=%d"+
			" companions_copied=%d companions_skipped=%d companions_renamed=%d companions_errors=%d"+
			" companions_bytes_copied=%d companions_bytes_skipped=%d"+
			" dry_run=%t interrupted=%t\n",
		s.Scanned, s.Unreadable, s.Groups, s.Copied, s.Skipped, s.Renamed, s.Errors,
		s.Moved, s.BytesCopied, s.BytesSkipped,
		s.CompanionsCopied, s.CompanionsSkipped, s.CompanionsRenamed, s.CompanionsErrors,
		s.CompanionsBytesCopied, s.CompanionsBytesSkipped,
		cfg.DryRun, interrupted)
	if err != nil {
		return fmt.Errorf("writing summary: %w", err)
	}
	return nil
}

// emitClean writes the result of a `clean` run to stdout.
func (r *reporter) emitClean(cfg config.CleanConfig, sum clean.Summary, interrupted bool) error {
	s := cleanSummary{
		Deleted:      sum.Deleted,
		WouldDelete:  sum.WouldDelete,
		Kept:         sum.Kept,
		Errors:       sum.Errors,
		SourceHashed: sum.SourceFilesHashed,
		DestHashed:   sum.DestFilesHashed,
	}
	if r.json() {
		return r.writeJSON(cleanReport{
			Command: "clean", Source: cfg.Source, Dest: cfg.DestRoot,
			Delete: cfg.Delete, Interrupted: interrupted,
			Results: r.clean, Summary: s,
		})
	}
	_, err := fmt.Fprintf(r.stdout,
		"deleted=%d would_delete=%d kept=%d errors=%d source_hashed=%d dest_hashed=%d"+
			" delete=%t interrupted=%t\n",
		s.Deleted, s.WouldDelete, s.Kept, s.Errors, s.SourceHashed, s.DestHashed,
		cfg.Delete, interrupted)
	if err != nil {
		return fmt.Errorf("writing summary: %w", err)
	}
	return nil
}

// emitUndo writes the result of an `undo` run to stdout.
func (r *reporter) emitUndo(cfg config.UndoConfig, sum undo.Summary, interrupted bool) error {
	s := undoSummary{
		Removed:     sum.Removed,
		WouldRemove: sum.WouldRemove,
		Kept:        sum.Kept,
		Errors:      sum.Errors,
		DirsPruned:  sum.DirsPruned,
	}
	if r.json() {
		return r.writeJSON(undoReport{
			Command: "undo", Dest: cfg.DestRoot,
			Delete: cfg.Delete, Interrupted: interrupted,
			Results: r.undo, Summary: s,
		})
	}
	_, err := fmt.Fprintf(r.stdout,
		"removed=%d would_remove=%d kept=%d errors=%d dirs_pruned=%d delete=%t interrupted=%t\n",
		s.Removed, s.WouldRemove, s.Kept, s.Errors, s.DirsPruned, cfg.Delete, interrupted)
	if err != nil {
		return fmt.Errorf("writing summary: %w", err)
	}
	return nil
}

// writeJSON marshals one document as a single line of JSON.
func (r *reporter) writeJSON(doc any) error {
	if err := json.NewEncoder(r.stdout).Encode(doc); err != nil {
		return fmt.Errorf("encoding json output: %w", err)
	}
	return nil
}

// versionReport is the stdout document of `version`.
type versionReport struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	Built     string `json:"built,omitempty"`
	Modified  bool   `json:"modified,omitempty"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
}

// buildReport assembles the version document. The -ldflags version stays
// authoritative for the release tag; the rest is read back from the build itself via
// runtime/debug, so a binary can always say which commit it came from. Every VCS
// field is optional: a build made outside a repository (or with -buildvcs=false)
// simply carries none, and the fields are omitted rather than faked.
func buildReport(version string) versionReport {
	rep := versionReport{
		Version:   version,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return rep
	}
	if info.GoVersion != "" {
		rep.GoVersion = info.GoVersion
	}
	// A binary built straight from source carries no injected version; the module
	// version is the best answer available.
	if (rep.Version == "" || rep.Version == "dev") && info.Main.Version != "" {
		rep.Version = info.Main.Version
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rep.Commit = s.Value
		case "vcs.time":
			rep.Built = s.Value
		case "vcs.modified":
			rep.Modified = s.Value == "true"
		}
	}
	return rep
}

// emit writes the version document. The first line stays exactly "moraine <version>"
// so the terse root --version output remains its prefix.
func (v versionReport) emit(format config.OutputFormat, stdout io.Writer) error {
	if format == config.OutputJSON {
		enc := json.NewEncoder(stdout)
		if err := enc.Encode(v); err != nil {
			return fmt.Errorf("encoding json output: %w", err)
		}
		return nil
	}
	var b []byte
	b = fmt.Appendf(b, "moraine %s\n", v.Version)
	if v.Commit != "" {
		b = fmt.Appendf(b, "commit=%s\n", v.Commit)
	}
	if v.Modified {
		b = fmt.Appendf(b, "modified=true\n")
	}
	if v.Built != "" {
		b = fmt.Appendf(b, "built=%s\n", v.Built)
	}
	b = fmt.Appendf(b, "go=%s\nplatform=%s\n", v.GoVersion, v.Platform)
	if _, err := stdout.Write(b); err != nil {
		return fmt.Errorf("writing version: %w", err)
	}
	return nil
}

// configReport is the stdout document of `config show`, and of `config set`, `unset`
// and `edit` once they have decided what the file should hold. Every command in the
// config tree reports the same shape, so a script never has to know which one ran.
type configReport struct {
	Command  string          `json:"command"`
	Path     string          `json:"path"`
	Exists   bool            `json:"exists"`
	Written  bool            `json:"written"`
	DryRun   bool            `json:"dry_run"`
	Settings []configSetting `json:"settings"`
}

// configSetting is one effective setting: the value a run would use, where it came
// from, and what it would fall back to.
type configSetting struct {
	Key     string `json:"key"`
	Value   string `json:"value"`
	Origin  string `json:"origin"`
	Default string `json:"default"`
}

// The two places a setting's value can come from. There is deliberately no third:
// environment variables select which file is read, never what a setting is
// (Constitution Principle V).
const (
	originDefault = "default"
	originFile    = "file"
)

// emit writes the configuration document. The text rendering is one record per line
// in the same key=value shape as every other summary this tool prints, with the
// origin as a second pair so that "where did this come from?" is answered without a
// second command; --origins=false drops it for a bare listing.
func (c configReport) emit(format config.OutputFormat, origins bool, stdout io.Writer) error {
	if format == config.OutputJSON {
		if err := json.NewEncoder(stdout).Encode(c); err != nil {
			return fmt.Errorf("encoding json output: %w", err)
		}
		return nil
	}
	var b []byte
	for _, s := range c.Settings {
		if origins {
			b = fmt.Appendf(b, "%s=%s origin=%s\n", s.Key, s.Value, s.Origin)
			continue
		}
		b = fmt.Appendf(b, "%s=%s\n", s.Key, s.Value)
	}
	if _, err := stdout.Write(b); err != nil {
		return fmt.Errorf("writing configuration: %w", err)
	}
	return nil
}

// pathReport is the stdout document of `config path`: which file is in effect, and
// why that one. It answers "why did my setting not apply?" without guesswork.
type pathReport struct {
	Command string `json:"command"`
	Path    string `json:"path"`
	Exists  bool   `json:"exists"`
	Source  string `json:"source"`
}

// emit writes the path document.
func (p pathReport) emit(format config.OutputFormat, stdout io.Writer) error {
	if format == config.OutputJSON {
		if err := json.NewEncoder(stdout).Encode(p); err != nil {
			return fmt.Errorf("encoding json output: %w", err)
		}
		return nil
	}
	if _, err := fmt.Fprintf(stdout, "path=%s exists=%t source=%s\n", p.Path, p.Exists, p.Source); err != nil {
		return fmt.Errorf("writing configuration path: %w", err)
	}
	return nil
}
