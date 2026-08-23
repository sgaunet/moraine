package organize_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/organize"
)

// aug12 is the date the rest of the organize tests use, so a template rendering can
// be compared against the layout those tests already pin.
var aug12 = time.Date(2025, 8, 12, 10, 0, 0, 0, time.UTC)

func TestDefaultTemplateReproducesTheHistoricalLayout(t *testing.T) {
	tmpl, err := organize.ParseTemplate(organize.DefaultTemplate)
	if err != nil {
		t.Fatalf("the default template must parse: %v", err)
	}
	if got, want := tmpl.Render("nature", aug12), filepath.Join("nature", "2025", "2025-08-12"); got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
	if got, want := tmpl.Render("nature", time.Time{}), filepath.Join("nature", "unknown-date"); got != want {
		t.Errorf("undated Render() = %q, want %q", got, want)
	}
}

// The zero Template is what an Organizer has when nobody sets one, so it must be
// the default layout rather than an empty path.
func TestZeroTemplateRendersTheDefault(t *testing.T) {
	var zero organize.Template
	parsed, err := organize.ParseTemplate(organize.DefaultTemplate)
	if err != nil {
		t.Fatalf("ParseTemplate: %v", err)
	}
	if got, want := zero.Render("cook", aug12), parsed.Render("cook", aug12); got != want {
		t.Errorf("zero Template Render() = %q, want %q", got, want)
	}
	if got, want := zero.String(), organize.DefaultTemplate; got != want {
		t.Errorf("zero Template String() = %q, want %q", got, want)
	}
}

// An empty or blank flag value means "unset", matching how config treats an empty
// --exiftool, so it must not be an error.
func TestParseTemplateTreatsBlankAsUnset(t *testing.T) {
	for _, in := range []string{"", "   ", "\t"} {
		tmpl, err := organize.ParseTemplate(in)
		if err != nil {
			t.Fatalf("ParseTemplate(%q) = %v, want no error", in, err)
		}
		if got, want := tmpl.String(), organize.DefaultTemplate; got != want {
			t.Errorf("ParseTemplate(%q).String() = %q, want %q", in, got, want)
		}
	}
}

func TestTemplateRenders(t *testing.T) {
	tests := []struct {
		name  string
		tmpl  string
		theme string
		date  time.Time
		want  []string // path elements, joined for the OS
	}{
		{"default", "{theme}/{year}/{date}", "nature", aug12, []string{"nature", "2025", "2025-08-12"}},
		{"year and month", "{theme}/{year}/{month}", "cook", aug12, []string{"cook", "2025", "08"}},
		{"flat by year", "{year}", "cook", aug12, []string{"2025"}},
		{"theme only", "{theme}", "family", aug12, []string{"family"}},
		{"theme last", "{year}/{month}-{day}/{theme}", "family", aug12, []string{"2025", "08-12", "family"}},
		{"literal prefix", "photos/{theme}/{date}", "cook", aug12, []string{"photos", "cook", "2025-08-12"}},
		{"literals around a token", "y{year}y", "cook", aug12, []string{"y2025y"}},
		{"day and month only", "{month}/{day}", "cook", aug12, []string{"08", "12"}},
		{"repeated token", "{year}/{year}", "cook", aug12, []string{"2025", "2025"}},
		{"hidden but unreserved", ".archive/{theme}", "cook", aug12, []string{".archive", "cook"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpl, err := organize.ParseTemplate(tc.tmpl)
			if err != nil {
				t.Fatalf("ParseTemplate(%q) = %v", tc.tmpl, err)
			}
			if got, want := tmpl.Render(tc.theme, tc.date), filepath.Join(tc.want...); got != want {
				t.Errorf("Render() = %q, want %q", got, want)
			}
		})
	}
}

// An unknown date collapses each run of date-derived segments to one "unknown-date",
// so a template's non-date segments keep their positions and no path ever spells
// "unknown-date/unknown-date".
func TestTemplateUnknownDateCollapsesEachRun(t *testing.T) {
	tests := []struct {
		name string
		tmpl string
		want []string
	}{
		{"default", "{theme}/{year}/{date}", []string{"nature", "unknown-date"}},
		{"three date segments", "{theme}/{year}/{month}/{day}", []string{"nature", "unknown-date"}},
		{"date first", "{year}/{month}/{theme}", []string{"unknown-date", "nature"}},
		{"date only", "{year}/{date}", []string{"unknown-date"}},
		{"two separate runs", "{year}/{theme}/{month}", []string{"unknown-date", "nature", "unknown-date"}},
		{"no date at all", "{theme}", []string{"nature"}},
		{"literal mixed into a date segment", "{theme}/y{year}", []string{"nature", "unknown-date"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpl, err := organize.ParseTemplate(tc.tmpl)
			if err != nil {
				t.Fatalf("ParseTemplate(%q) = %v", tc.tmpl, err)
			}
			if got, want := tmpl.Render("nature", time.Time{}), filepath.Join(tc.want...); got != want {
				t.Errorf("Render() = %q, want %q", got, want)
			}
		})
	}
}

func TestParseTemplateRejects(t *testing.T) {
	tests := []struct {
		name string
		tmpl string
	}{
		{"unknown placeholder", "{theme}/{bogus}"},
		{"unknown placeholder alone", "{camera}"},
		{"unclosed brace", "{theme}/{year"},
		{"absolute path", "/{theme}/{year}"},
		{"empty segment", "{theme}//{year}"},
		{"trailing slash leaves an empty segment", "{theme}/"},
		{"dot segment", "{theme}/./{year}"},
		{"parent segment", "{theme}/../{year}"},
		{"escaping parent", "../{theme}"},
		{"reserved bookkeeping dir", ".moraine/{theme}"},
		{"reserved bookkeeping prefix", ".moraine-tmp/{theme}"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := organize.ParseTemplate(tc.tmpl); err == nil {
				t.Fatalf("ParseTemplate(%q) = nil error, want a rejection", tc.tmpl)
			}
		})
	}
}

// A ".moraine" segment is only dangerous as the first one: the bookkeeping tree is
// <dest>/.moraine, so the same name inside a theme folder is an ordinary directory.
func TestParseTemplateAllowsReservedNameBelowTheFirstSegment(t *testing.T) {
	tmpl, err := organize.ParseTemplate("{theme}/.moraine")
	if err != nil {
		t.Fatalf("ParseTemplate = %v, want no error", err)
	}
	if got, want := tmpl.Render("cook", aug12), filepath.Join("cook", ".moraine"); got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
}

// String reports the template as written, which is what the manifest header records
// and what the incremental-change warning quotes.
func TestTemplateStringRoundTrips(t *testing.T) {
	const in = "{year}/{month}/{theme}"
	tmpl, err := organize.ParseTemplate(in)
	if err != nil {
		t.Fatalf("ParseTemplate: %v", err)
	}
	if got := tmpl.String(); got != in {
		t.Errorf("String() = %q, want %q", got, in)
	}
}

// Rendering must not depend on how many times it has run.
func TestTemplateRenderIsRepeatable(t *testing.T) {
	tmpl, err := organize.ParseTemplate("{theme}/{year}/{month}")
	if err != nil {
		t.Fatalf("ParseTemplate: %v", err)
	}
	first := tmpl.Render("cook", aug12)
	for range 3 {
		if got := tmpl.Render("cook", aug12); got != first {
			t.Fatalf("Render() = %q on a later call, want %q", got, first)
		}
	}
}

func TestPlaceHonoursANonDefaultTemplate(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	c := clusterOf(t, src, "IMG_1.jpg")

	org := organize.New(dest)
	tmpl, err := organize.ParseTemplate("{year}/{month}/{theme}")
	if err != nil {
		t.Fatalf("ParseTemplate: %v", err)
	}
	org.Template = tmpl

	results := org.Place(context.Background(), c, "nature")
	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("want one clean result, got %+v", results)
	}
	wantDir := filepath.Join(dest, "2025", "08", "nature")
	if got := filepath.Dir(results[0].Dest); got != wantDir {
		t.Fatalf("dir = %q, want %q", got, wantDir)
	}
	if _, err := os.Stat(results[0].Dest); err != nil {
		t.Fatalf("dest missing: %v", err)
	}
	// The default layout must not also have been created.
	if _, err := os.Stat(filepath.Join(dest, "nature")); err == nil {
		t.Error("the default layout's theme folder was created too")
	}
}

// The destination directory is created on first use, so a cluster whose every file
// the manifest already accounts for leaves no new empty folder behind. Before the
// directory was made lazily, an incremental re-run littered the destination with
// one empty folder per event it skipped.
func TestPlaceCreatesNoDirectoryWhenEverythingIsAlreadyPlaced(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	c := clusterOf(t, src, "IMG_1.jpg")
	photoPath := c.Photos[0].Path

	// The recorded copy lives outside the layout this run would use, so the absence
	// of the layout folder afterwards is unambiguous.
	placedDest := filepath.Join(dest, "recorded", "IMG_1.jpg")
	if err := os.MkdirAll(filepath.Dir(placedDest), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, placedDest, "content-IMG_1.jpg")
	size, mtime := fingerprint(t, photoPath)
	if err := os.Chtimes(placedDest, time.Time{}, mtime); err != nil {
		t.Fatal(err)
	}

	org := placedOrg(dest, map[string]organize.Placement{
		photoPath: {Dest: placedDest, Size: size, ModTime: mtime},
	})
	results := org.Place(context.Background(), c, "family")
	if len(results) != 1 || results[0].Action != organize.ActionSkippedIdentical {
		t.Fatalf("want one skipped-identical result, got %+v", results)
	}
	if _, err := os.Stat(filepath.Join(dest, "family")); err == nil {
		t.Error("an all-skipped cluster created its destination folder anyway")
	}
}
