package organize

// Test-only re-exports of unexported helpers so black-box tests
// (package organize_test) can exercise them.
var (
	SafeJoin    = safeJoin
	UniqueName  = uniqueName
	CopyFile    = copyFile
	SameContent = sameContent
)
