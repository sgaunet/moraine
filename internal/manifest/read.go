package manifest

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// maxLine bounds one manifest line. Paths and error strings are the only variable
// part of a record, so this is generous by orders of magnitude; it exists so a
// corrupt file cannot make the reader allocate without bound.
const maxLine = 1 << 20

// Run is one recorded run, read back from its manifest file.
type Run struct {
	Path    string   // the manifest file this run was read from
	Header  Header   // the file's header line
	Records []Record // the placements, in the order they happened
	Skipped int      // lines that could not be parsed (a killed run truncates its last)
}

// ID returns the run's identifier, which is also its file name without extension.
func (r Run) ID() string { return r.Header.Run }

// Files returns the manifest files under destRoot, oldest first (run ids sort
// chronologically). Runs already undone are left out — see undoneExt. A
// destination that has never been sorted has no manifests, which is not an error.
func Files(destRoot string) ([]string, error) {
	dir := filepath.Join(destRoot, dirName, runsName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading manifest directory %q: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ext) {
			continue // .undone runs and stray files are not runs to act on
		}
		names = append(names, e.Name())
	}
	// Sort by run id, not by file name: a same-second run takes a "-1" suffix, and
	// '-' sorts before '.', so ordering whole names would put the *newer* run first
	// and hand `undo` the wrong one. Stems put the plain id ahead of its suffixed
	// variants, which is the order the runs happened in.
	sort.Slice(names, func(i, j int) bool {
		return runID(names[i]) < runID(names[j])
	})
	files := make([]string, 0, len(names))
	for _, n := range names {
		files = append(files, filepath.Join(dir, n))
	}
	return files, nil
}

// runID is a manifest file name without its extension.
func runID(name string) string { return strings.TrimSuffix(name, ext) }

// Latest returns the most recent manifest file under destRoot, or "" when none
// was ever recorded.
func Latest(destRoot string) (string, error) {
	files, err := Files(destRoot)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", nil
	}
	return files[len(files)-1], nil
}

// ReadRun parses one manifest file. A line that cannot be parsed is counted in
// Skipped rather than failing the read: a run killed mid-write leaves a truncated
// final line, and everything it did place before that must stay recoverable. A
// missing or unrecognised header, on the other hand, means this is not a moraine
// manifest and is an error — acting on a file we cannot identify is exactly what
// an undo must never do.
func ReadRun(path string) (Run, error) {
	f, err := os.Open(path)
	if err != nil {
		return Run{}, fmt.Errorf("opening manifest %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, bufio.MaxScanTokenSize), maxLine)

	run := Run{Path: path}
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return Run{}, fmt.Errorf("reading manifest %q: %w", path, err)
		}
		return Run{}, fmt.Errorf("manifest %q is empty", path)
	}
	if err := json.Unmarshal(sc.Bytes(), &run.Header); err != nil || run.Header.Manifest == 0 {
		return Run{}, fmt.Errorf("%q is not a moraine manifest (no schema header)", path)
	}
	if run.Header.Manifest > SchemaVersion {
		return Run{}, fmt.Errorf("manifest %q has schema version %d, newer than this moraine understands (%d)",
			path, run.Header.Manifest, SchemaVersion)
	}

	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil || rec.Source == "" {
			run.Skipped++
			continue
		}
		run.Records = append(run.Records, rec)
	}
	if err := sc.Err(); err != nil {
		return Run{}, fmt.Errorf("reading manifest %q: %w", path, err)
	}
	return run, nil
}

// MarkUndone renames a manifest file so Files no longer reports it, after `undo`
// has removed everything it recorded. The file is kept, not deleted: it stays the
// audit trail of a run that happened.
func MarkUndone(path string) error {
	if err := os.Rename(path, strings.TrimSuffix(path, ext)+undoneExt); err != nil {
		return fmt.Errorf("marking manifest %q undone: %w", path, err)
	}
	return nil
}

// Index answers "have I already placed this source, and where?" across every run
// recorded under a destination. It is what makes an incremental run cheap: a hit
// replaces a full byte comparison with a size + mtime check.
type Index struct {
	bySource map[string]Record
	// Skipped counts manifest files that could not be read at all, so the caller
	// can say the index is partial instead of silently under-reporting.
	Skipped int
	// PathTemplate is the destination layout the newest readable run recorded, or ""
	// when no run recorded one. A caller compares it with the layout it is about to
	// use so it can warn that recorded files stay where they already are.
	PathTemplate string
}

// Load folds every manifest under destRoot into an Index, oldest run first so the
// most recent placement of a source wins. Failed placements are not indexed: only
// a record with a destination describes a file that exists.
func Load(destRoot string) (*Index, error) {
	files, err := Files(destRoot)
	if err != nil {
		return nil, err
	}
	idx := &Index{bySource: make(map[string]Record)}
	for _, f := range files {
		run, err := ReadRun(f)
		if err != nil {
			idx.Skipped++
			continue
		}
		// Files are oldest first, so the last one to set this wins.
		if run.Header.PathTemplate != "" {
			idx.PathTemplate = run.Header.PathTemplate
		}
		for _, rec := range run.Records {
			if rec.Dest == "" || rec.Error != "" {
				continue
			}
			idx.bySource[rec.Source] = rec
		}
	}
	return idx, nil
}

// Lookup returns the most recent placement recorded for a source path.
func (i *Index) Lookup(source string) (Record, bool) {
	if i == nil {
		return Record{}, false
	}
	rec, ok := i.bySource[source]
	return rec, ok
}

// Len reports how many distinct sources the index knows about.
func (i *Index) Len() int {
	if i == nil {
		return 0
	}
	return len(i.bySource)
}
