package rawpreview

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// verTimeout bounds the startup availability probe so a wedged binary cannot
// hang the program before it does any work.
const verTimeout = 5 * time.Second

// EnsureAvailable verifies that exiftool (named by path, or "exiftool" on PATH
// when path is empty) is present and runnable, by resolving it and running
// "<path> -ver" under a short timeout. It returns nil when exiftool is usable,
// or an actionable error otherwise. exiftool is mandatory: callers run this at
// startup and abort the program on a non-nil result (FR-003).
func EnsureAvailable(path string) error {
	if path == "" {
		path = "exiftool"
	}

	resolved := path
	if strings.ContainsRune(path, os.PathSeparator) {
		info, err := os.Stat(path)
		if err != nil {
			return notAvailable(path, err)
		}
		if info.IsDir() {
			return notAvailable(path, fmt.Errorf("%q is a directory", path))
		}
	} else {
		p, err := exec.LookPath(path)
		if err != nil {
			return notAvailable(path, err)
		}
		resolved = p
	}

	ctx, cancel := context.WithTimeout(context.Background(), verTimeout)
	defer cancel()
	if err := exec.CommandContext(ctx, resolved, "-ver").Run(); err != nil {
		return notAvailable(path, err)
	}
	return nil
}

// notAvailable wraps the underlying cause in a message that names exiftool, says
// how to install it, and points at the -exiftool flag (Constitution VI).
func notAvailable(path string, cause error) error {
	return fmt.Errorf("exiftool is required to read RAW files but %q is not usable: %w\n"+
		"Install it (macOS: `brew install exiftool`; Debian/Ubuntu: `sudo apt install libimage-exiftool-perl`) "+
		"or pass its path with -exiftool <path>", path, cause)
}
