// Package manifest records what a `sort` run placed, as one JSON Lines file per
// run under the destination root. It is the keystone the reversible and
// incremental parts of moraine stand on: `undo` reads a run's manifest to remove
// exactly the files that run created, and an incremental `sort` reads every
// manifest to recognise sources it has already placed without re-reading their
// bytes.
//
// One file per run (rather than one growing log) keeps the unit of undo obvious
// and makes an interrupted run's partial record self-contained. The format is a
// header line followed by one record per placed file, so a run can be appended to
// as it goes and read back after a crash by discarding a truncated final line.
//
// Pure filesystem logic — no transport, no global state (Constitution Principle
// III).
package manifest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DateFormat is how a record's representative date is written, matching the stdout
// contract's date field. It is independent of the destination layout, which
// --path-template can change.
const DateFormat = "2006-01-02"

// SchemaVersion is written in every manifest header. A reader that meets a
// version it does not know can say so instead of guessing.
const SchemaVersion = 1

// Layout of the bookkeeping tree under the destination root. The leading dot
// keeps it out of the way of the theme folders beside it, which are always
// [a-z0-9-] slugs and so can never collide with it.
const (
	dirName  = ".moraine"
	runsName = "runs"
	ext      = ".jsonl"
	// undoneExt marks a run whose files `undo` has removed. Such a run is skipped
	// by Files, so a second undo steps back to the run before it instead of
	// re-reporting the same one.
	undoneExt = ext + ".undone"
	// runFormat names a run after the UTC second it started in, which sorts
	// lexicographically in chronological order.
	runFormat = "20060102T150405Z"
)

// Header is the first line of a manifest file: which run wrote it, from where, to
// where, and under which schema.
type Header struct {
	Manifest int    `json:"manifest"`
	Run      string `json:"run"`
	Source   string `json:"source"`
	Dest     string `json:"dest"`
	Started  string `json:"started"`
	// PathTemplate is the destination layout the run placed files under. It lets a
	// later --incremental run notice that the layout changed since — recorded files
	// keep the path they were given, so the two layouts coexist. Empty on manifests
	// written before templates existed, which reads as "unknown", not "the default".
	PathTemplate string `json:"path_template,omitempty"`
}

// Record is one placed file — a photo or one of its companions. Size and MTime
// fingerprint the file as it was left on disk; because copies preserve the
// source's modification time, the same pair also identifies the source that
// produced it. That is what lets `undo` refuse to delete a file that changed
// since the run, and an incremental run recognise an unchanged source, without
// hashing anything.
type Record struct {
	Source    string `json:"source"`
	Dest      string `json:"dest,omitempty"`
	Theme     string `json:"theme,omitempty"`
	Date      string `json:"date,omitempty"`
	Action    string `json:"action,omitempty"`
	Companion bool   `json:"companion,omitempty"`
	// Moved records that --move removed this file's source after verifying the copy.
	// `undo` refuses to remove such a copy: the original is gone, so this is the only
	// remaining file. A run predating --move has no such records.
	Moved bool   `json:"moved,omitempty"`
	Of    string `json:"of,omitempty"`
	Size  int64  `json:"size,omitempty"`
	MTime int64  `json:"mtime,omitempty"`
	Error string `json:"error,omitempty"`
}

// Writer appends the records of one run to its own manifest file. The file is
// created by the first Add, so a dry run — or a run that places nothing — leaves
// no trace at all, not even a directory.
//
// A nil *Writer is a working no-op writer: the caller that has no manifest to
// write (a dry run) holds nil instead of branching at every record.
type Writer struct {
	// PathTemplate is the destination layout this run used, written into the header.
	// Set it before the first Add, in the public-field style organize.Organizer uses.
	PathTemplate string

	dir    string // <destRoot>/.moraine/runs
	header Header
	stamp  string // run-id base derived from the run's start time
	path   string
	file   *os.File
	buf    *bufio.Writer
}

// New returns a Writer for a run of source into destRoot started at now. Nothing
// is created or written until the first Add.
func New(destRoot, source string, now time.Time) *Writer {
	utc := now.UTC()
	return &Writer{
		dir:   filepath.Join(destRoot, dirName, runsName),
		stamp: utc.Format(runFormat),
		header: Header{
			Manifest: SchemaVersion,
			Source:   source,
			Dest:     destRoot,
			Started:  utc.Format(time.RFC3339),
		},
	}
}

// Path returns the manifest file's path, or "" while nothing has been written.
func (w *Writer) Path() string {
	if w == nil {
		return ""
	}
	return w.path
}

// Add appends one record, creating the manifest file on first use.
func (w *Writer) Add(r Record) error {
	if w == nil {
		return nil
	}
	if w.file == nil {
		if err := w.open(); err != nil {
			return err
		}
	}
	return w.writeLine(r)
}

// Close flushes the buffered records and makes them durable. A Writer that never
// created its file has nothing to close.
func (w *Writer) Close() error {
	if w == nil || w.file == nil {
		return nil
	}
	defer func() {
		_ = w.file.Close()
		w.file = nil
	}()
	if err := w.buf.Flush(); err != nil {
		return fmt.Errorf("flushing manifest %q: %w", w.path, err)
	}
	if err := w.file.Sync(); err != nil {
		return fmt.Errorf("fsync manifest %q: %w", w.path, err)
	}
	return nil
}

// open creates the run's file under a name no other run holds and writes the
// header. Two runs started in the same second would otherwise share a name, so
// the first free " -N" variant is taken — O_EXCL, not a prior existence check,
// is what makes that safe.
func (w *Writer) open() error {
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return fmt.Errorf("creating manifest directory %q: %w", w.dir, err)
	}
	for i := 0; ; i++ {
		name := w.stamp + ext
		if i > 0 {
			name = fmt.Sprintf("%s-%d%s", w.stamp, i, ext)
		}
		path := filepath.Join(w.dir, name)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			if os.IsExist(err) {
				continue
			}
			return fmt.Errorf("creating manifest %q: %w", path, err)
		}
		w.file, w.buf, w.path = f, bufio.NewWriter(f), path
		w.header.Run = name[:len(name)-len(ext)]
		w.header.PathTemplate = w.PathTemplate
		return w.writeLine(w.header)
	}
}

// writeLine appends one JSON document as a single line.
func (w *Writer) writeLine(doc any) error {
	b, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("encoding manifest record: %w", err)
	}
	if _, err := w.buf.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("writing manifest %q: %w", w.path, err)
	}
	return nil
}
