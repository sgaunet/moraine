package exifmeta

import (
	"regexp"
	"strconv"
	"time"
)

// Filename dating sits between EXIF and the file mtime (see Read). It exists
// because a batch of downloaded photos — WhatsApp, screenshots, an export — carries
// no EXIF and shares one mtime, so dating it by mtime collapses months of pictures
// into a single "event" stamped with the download day. The camera app that produced
// them, however, wrote the capture date into the file name.
//
// The patterns are deliberately narrow: a four-digit 19xx/20xx year followed by a
// month and a day, optionally followed by a clock reading. That is what keeps a
// frame counter (IMG_1234.jpg) or a model name (DSC_2019.jpg) from being read as a
// date. Go's regexp is RE2, which has no backreferences, so the separators are
// matched independently rather than required to be identical.
const (
	// datePart matches YYYYMMDD with optional separators: 2023-08-15, 2023_08_15, 20230815.
	datePart = `((?:19|20)\d{2})[-_.]?(\d{2})[-_.]?(\d{2})`
	// timePart matches HHMMSS with optional separators: 120000, 12.00.00, 12:00:00.
	timePart = `(\d{2})[-_.:]?(\d{2})[-_.:]?(\d{2})`
	// datePartSep is what may sit between the two: a run of separators, or the
	// " at " macOS writes into a screenshot name.
	datePartSep = `(?:[-_.T ]|at |at_)*`
)

var (
	// dateTimeRe matches IMG_20230815_120000.jpg, PXL_20230815_120000123.jpg,
	// Screenshot_20230815-120000.png, "Screenshot 2023-08-15 at 12.00.00.png".
	dateTimeRe = regexp.MustCompile(`(?:^|[^0-9])` + datePart + datePartSep + timePart)
	// dateRe matches a bare date, as WhatsApp writes it: IMG-20230815-WA0001.jpg.
	dateRe = regexp.MustCompile(`(?:^|[^0-9])` + datePart)
)

// dateFromName returns the capture date encoded in a file name, as a UTC-naive wall
// clock (the frame described in the package doc), or the zero time when the name
// carries no plausible date. A name with a date but no clock reading dates to
// midnight, which is enough to put a day's worth of downloads in its own event.
//
// A date-with-time match that is not a real calendar reading falls through to the
// date-only attempt, so a mangled clock never costs an otherwise usable date.
func dateFromName(name string) time.Time {
	if m := dateTimeRe.FindStringSubmatch(name); m != nil {
		if t, ok := calendarDate(m[1], m[2], m[3], m[4], m[5], m[6]); ok {
			return t
		}
	}
	if m := dateRe.FindStringSubmatch(name); m != nil {
		if t, ok := calendarDate(m[1], m[2], m[3], "0", "0", "0"); ok {
			return t
		}
	}
	return time.Time{}
}

// calendarDate builds a UTC time from matched digit groups and reports whether they
// name a real date and clock reading. time.Date normalises out-of-range values
// (month 13 becomes January of the next year), so reading the fields back out is
// the validation.
func calendarDate(year, month, day, hour, minute, second string) (time.Time, bool) {
	y, mo, d := atoi(year), atoi(month), atoi(day)
	h, mi, s := atoi(hour), atoi(minute), atoi(second)
	t := time.Date(y, time.Month(mo), d, h, mi, s, 0, time.UTC)
	if t.Year() != y || int(t.Month()) != mo || t.Day() != d ||
		t.Hour() != h || t.Minute() != mi || t.Second() != s {
		return time.Time{}, false
	}
	return t, true
}

// atoi converts a group the regexp has already proved to be ASCII digits.
func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
