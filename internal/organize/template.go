package organize

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultTemplate is the destination layout moraine has always used, and the one a
// run gets when --path-template is not given. Keeping it spelled out here (rather
// than hard-coded inside dir) is what lets the template tests prove that the
// default reproduces the historical layout exactly.
const DefaultTemplate = "{theme}/{year}/{date}"

// unknownDateDir replaces the date-derived part of the path for a cluster with no
// usable capture time. Formatting a zero time would file those photos under
// "0001/0001-01-01", a folder that looks like a real date and hides the fact that
// the date is simply unknown. Four-digit years cannot collide with this name.
const unknownDateDir = "unknown-date"

// reservedPrefix is the destination's bookkeeping prefix: the .moraine/ run-manifest
// tree and the .moraine-*.tmp staging files. internal/manifest relies on theme
// folders being [a-z0-9-] slugs and so never colliding with it, an argument a
// user-supplied template could otherwise break. It is spelled literally rather than
// imported so that organize keeps no dependency on the manifest package.
const reservedPrefix = ".moraine"

// token is one recognised placeholder. layout is the time layout that renders it;
// the empty layout marks {theme}, the only placeholder that is not date-derived.
type token struct {
	name   string
	layout string
}

// knownTokens is the complete, closed set of placeholders, in the order an error
// message lists them. Deliberately small: every addition is a new thing to
// document, validate and support forever.
var knownTokens = []token{
	{name: "theme", layout: ""},
	{name: "year", layout: "2006"},
	{name: "month", layout: "01"},
	{name: "day", layout: "02"},
	{name: "date", layout: "2006-01-02"},
}

// part is one piece of a segment: literal text, or a placeholder to substitute.
type part struct {
	literal string
	tok     *token
}

// segment is one path component of a template. hasDate records whether any of its
// placeholders is date-derived, which is what Render collapses for an unknown date.
type segment struct {
	parts   []part
	hasDate bool
}

// Template is a validated destination-path template: a "/"-separated list of
// segments, each mixing literal text with {theme}, {year}, {month}, {day} and
// {date} placeholders. The zero Template renders DefaultTemplate, so an Organizer
// that never sets one keeps the historical layout.
type Template struct {
	segments []segment
	raw      string
}

// defaultTemplate parses DefaultTemplate once. It cannot fail — the input is a
// constant this package owns, and TestDefaultTemplateParses pins that.
var defaultTemplate = sync.OnceValue(func() Template {
	t, err := ParseTemplate(DefaultTemplate)
	if err != nil {
		panic("organize: DefaultTemplate is invalid: " + err.Error())
	}
	return t
})

// ParseTemplate validates s and compiles it for rendering. An empty or
// whitespace-only s yields the zero Template (the default layout), matching how
// config treats an empty --exiftool.
//
// Validation is deliberately front-loaded here, so a bad template is rejected as a
// usage error before the run touches a single file rather than once per cluster.
// Every rejection closes a gap safeJoin does not: safeJoin blocks absolute paths
// and ".." escapes, but would happily accept an empty subdirectory (filing photos
// into the destination root) or a ".moraine" first segment.
func ParseTemplate(s string) (Template, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return Template{}, nil
	}
	if filepath.IsAbs(raw) || strings.HasPrefix(raw, "/") {
		return Template{}, fmt.Errorf("path template %q must be a relative path", raw)
	}

	fields := strings.Split(raw, "/")
	segs := make([]segment, 0, len(fields))
	for _, f := range fields {
		if f == "" {
			return Template{}, fmt.Errorf("path template %q has an empty path segment", raw)
		}
		if f == "." || f == ".." {
			return Template{}, fmt.Errorf("path template %q must not contain a %q segment", raw, f)
		}
		seg, err := parseSegment(f, raw)
		if err != nil {
			return Template{}, err
		}
		segs = append(segs, seg)
	}

	// Only the first segment can collide with the bookkeeping tree: a ".moraine"
	// deeper in the path is an ordinary directory inside a theme folder.
	if lit, ok := segs[0].literalOnly(); ok && strings.HasPrefix(lit, reservedPrefix) {
		return Template{}, fmt.Errorf(
			"path template %q must not start with %q: %q is reserved for moraine's own bookkeeping",
			raw, lit, reservedPrefix)
	}
	return Template{segments: segs, raw: raw}, nil
}

// parseSegment splits one path component into literals and placeholders. raw is
// carried only so an error can quote the whole template the user typed.
func parseSegment(s, raw string) (segment, error) {
	var seg segment
	for i := 0; i < len(s); {
		open := strings.IndexByte(s[i:], '{')
		if open < 0 {
			seg.parts = append(seg.parts, part{literal: s[i:]})
			break
		}
		open += i
		if open > i {
			seg.parts = append(seg.parts, part{literal: s[i:open]})
		}
		end := strings.IndexByte(s[open:], '}')
		if end < 0 {
			return segment{}, fmt.Errorf("path template %q has an unclosed \"{\"", raw)
		}
		end += open
		name := s[open+1 : end]
		tok := lookupToken(name)
		if tok == nil {
			return segment{}, fmt.Errorf(
				"path template %q has unknown placeholder %q; valid placeholders are %s",
				raw, "{"+name+"}", tokenNames())
		}
		seg.parts = append(seg.parts, part{tok: tok})
		if tok.layout != "" {
			seg.hasDate = true
		}
		i = end + 1
	}
	return seg, nil
}

// lookupToken returns the placeholder called name, or nil when there is none.
func lookupToken(name string) *token {
	for i := range knownTokens {
		if knownTokens[i].name == name {
			return &knownTokens[i]
		}
	}
	return nil
}

// tokenNames lists every placeholder, for an error message.
func tokenNames() string {
	names := make([]string, len(knownTokens))
	for i := range knownTokens {
		names[i] = "{" + knownTokens[i].name + "}"
	}
	return strings.Join(names, " ")
}

// literalOnly returns the segment's text when it holds no placeholders at all.
func (s segment) literalOnly() (string, bool) {
	if len(s.parts) == 1 && s.parts[0].tok == nil {
		return s.parts[0].literal, true
	}
	return "", false
}

// render substitutes theme and date into one segment.
func (s segment) render(theme string, date time.Time) string {
	var b strings.Builder
	for _, p := range s.parts {
		switch {
		case p.tok == nil:
			b.WriteString(p.literal)
		case p.tok.layout == "":
			b.WriteString(theme)
		default:
			b.WriteString(date.Format(p.tok.layout))
		}
	}
	return b.String()
}

// Render builds the destination subdirectory for a theme and a representative date.
//
// A zero date means no capture time could be determined. Rather than substituting
// per placeholder — which would spell "unknown-date/unknown-date" for the default
// template — each maximal run of consecutive date-derived segments collapses to a
// single "unknown-date" segment, leaving every other segment in place. So
// "{theme}/{year}/{date}" gives "<theme>/unknown-date" (exactly the historical
// layout) and "{year}/{month}/{theme}" gives "unknown-date/<theme>".
//
// The result is never empty: ParseTemplate rejects empty segments, and both a theme
// slug and a rendered date are non-empty.
func (t Template) Render(theme string, date time.Time) string {
	if len(t.segments) == 0 {
		t = defaultTemplate()
	}
	parts := make([]string, 0, len(t.segments))
	collapsed := false
	for _, seg := range t.segments {
		if date.IsZero() && seg.hasDate {
			if collapsed {
				continue // already covered by the unknown-date segment just emitted
			}
			parts = append(parts, unknownDateDir)
			collapsed = true
			continue
		}
		collapsed = false
		parts = append(parts, seg.render(theme, date))
	}
	return filepath.Join(parts...)
}

// String returns the template as the user wrote it, or the default when unset. It
// is what the run manifest records and what a log line quotes.
func (t Template) String() string {
	if t.raw == "" {
		return DefaultTemplate
	}
	return t.raw
}
