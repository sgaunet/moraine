package exifmeta

// Test-only re-export of the filename dating heuristic so the black-box tests
// (package exifmeta_test) can table-drive it directly, rather than inferring it
// from the file a Read happened to be given.
var DateFromName = dateFromName

// Test-only re-export of the panic boundary, so a test can hand it a reader that
// crashes on purpose. Going through Read would need a file that genuinely crashes
// a particular version of imagemeta, which is a moving target; the boundary itself
// is what the tests need to pin down.
var DecodeEXIF = decodeEXIF
