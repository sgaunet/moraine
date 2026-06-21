package organize

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sgaunet/moraine/internal/photo"
)

// Action records what happened when placing one photo.
type Action string

const (
	// ActionCopied means the photo was copied to a free destination path.
	ActionCopied Action = "copied"
	// ActionSkippedIdentical means a byte-identical file already existed; nothing was written.
	ActionSkippedIdentical Action = "skipped-identical"
	// ActionRenamed means a same-named but different file existed; the photo was copied under a suffixed name.
	ActionRenamed Action = "renamed"
)

// Result is the outcome of placing one photo.
type Result struct {
	Source string    // absolute source path
	Dest   string    // absolute destination path actually targeted (after any suffix)
	Theme  string    // theme slug used
	Date   time.Time // representative date used for <year>/<date>
	Action Action    // copied | skipped-identical | renamed
	Err    error     // non-nil on a placement failure (run continues)
}

// Organizer copies photos under a destination root using the
// <theme>/<year>/<year-month-day>/ layout.
type Organizer struct {
	DestRoot string
}

// New builds an Organizer writing under destRoot.
func New(destRoot string) *Organizer {
	return &Organizer{DestRoot: destRoot}
}

// Place copies every photo of the cluster into
// DestRoot/<theme>/<YYYY>/<YYYY-MM-DD>/<name>, using the cluster's representative
// date (c.Start) for all photos. It returns one Result per photo. A failure on
// one photo is recorded in its Result.Err and does not abort the others.
func (o *Organizer) Place(ctx context.Context, c photo.Cluster, theme string) []Result {
	results := make([]Result, 0, len(c.Photos))
	date := c.Start
	dir, dirErr := o.dir(theme, date)

	for _, p := range c.Photos {
		if err := ctx.Err(); err != nil {
			results = append(results, Result{Source: p.Path, Theme: theme, Date: date, Err: err})
			continue
		}
		res := Result{Source: p.Path, Theme: theme, Date: date}
		if dirErr != nil {
			res.Err = dirErr
			results = append(results, res)
			continue
		}
		res.Dest, res.Action, res.Err = o.placeOne(dir, p.Path, p.Name)
		results = append(results, res)
	}
	return results
}

// dir builds and creates the destination directory for a theme and date.
func (o *Organizer) dir(theme string, date time.Time) (string, error) {
	sub := filepath.Join(theme, date.Format("2006"), date.Format("2006-01-02"))
	dir, err := safeJoin(o.DestRoot, sub)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating directory %q: %w", dir, err)
	}
	return dir, nil
}

// placeOne copies a single source file into dir, resolving collisions: an
// identical existing file is skipped, a same-named different file is suffixed.
func (o *Organizer) placeOne(dir, src, name string) (string, Action, error) {
	target := filepath.Join(dir, name)
	if exists(target) {
		identical, err := sameContent(src, target)
		if err != nil {
			return target, "", fmt.Errorf("comparing %q: %w", target, err)
		}
		if identical {
			return target, ActionSkippedIdentical, nil
		}
		name = uniqueName(dir, name)
		target = filepath.Join(dir, name)
		if err := copyFile(src, target); err != nil {
			return target, "", err
		}
		return target, ActionRenamed, nil
	}
	if err := copyFile(src, target); err != nil {
		return target, "", err
	}
	return target, ActionCopied, nil
}
