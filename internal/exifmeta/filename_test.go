package exifmeta_test

import (
	"testing"
	"time"

	"github.com/sgaunet/moraine/internal/exifmeta"
)

func TestDateFromName(t *testing.T) {
	tests := []struct {
		name string
		file string
		want time.Time
	}{
		// Camera and phone conventions, date + time.
		{"android", "IMG_20230815_120000.jpg", aug15(12, 0, 0)},
		{"pixel with millis", "PXL_20230815_120000123.jpg", aug15(12, 0, 0)},
		{"bare", "20230815_120000.jpg", aug15(12, 0, 0)},
		{"screenshot dashed time", "Screenshot_20230815-120000.png", aug15(12, 0, 0)},
		{"macos screenshot", "Screenshot 2023-08-15 at 12.00.00.png", aug15(12, 0, 0)},
		{"separated date and time", "2023-08-15 12.00.00.jpg", aug15(12, 0, 0)},
		{"underscored date", "2023_08_15_235959.heic", aug15(23, 59, 59)},

		// Date only: a day's worth of downloads still becomes its own event.
		{"whatsapp", "IMG-20230815-WA0001.jpg", aug15(0, 0, 0)},
		{"date only", "20230815.jpg", aug15(0, 0, 0)},

		// A clock reading that is not a real time must not cost the date.
		{"impossible clock falls back to the date", "IMG_20230815_990000.jpg", aug15(0, 0, 0)},

		// Negatives: nothing that is merely digit-shaped may be read as a date.
		{"frame counter", "IMG_1234.jpg", time.Time{}},
		{"model name", "DSC_2019.jpg", time.Time{}},
		{"no digits", "vacation.jpg", time.Time{}},
		{"impossible month and day", "IMG_20231345_120000.jpg", time.Time{}},
		{"month zero", "IMG_20230015.jpg", time.Time{}},
		{"day zero", "IMG_20230800.jpg", time.Time{}},
		{"year out of range", "IMG_18230815_120000.jpg", time.Time{}},
		{"too few digits", "IMG_202308.jpg", time.Time{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := exifmeta.DateFromName(tc.file)
			if !got.Equal(tc.want) {
				t.Errorf("DateFromName(%q) = %s; want %s", tc.file, got, tc.want)
			}
			if !got.IsZero() && got.Location() != time.UTC {
				t.Errorf("DateFromName(%q) location = %v; want UTC", tc.file, got.Location())
			}
		})
	}
}

// aug15 is the capture date every positive case in the table encodes, at the given
// clock reading. A name carrying no clock reading dates to midnight.
func aug15(h, mi, s int) time.Time {
	return time.Date(2023, time.August, 15, h, mi, s, 0, time.UTC)
}
