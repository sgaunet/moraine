package exifmeta

// Test-only re-export of the filename dating heuristic so the black-box tests
// (package exifmeta_test) can table-drive it directly, rather than inferring it
// from the file a Read happened to be given.
var DateFromName = dateFromName
