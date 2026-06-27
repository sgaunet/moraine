// Command moraine organizes photos automatically: it scans a source directory
// (or a single file), groups photos into events by capture time, classifies each
// group into a theme, and copies the photos into
// destination/<theme>/<year>/<year-month-day>/. Originals are preserved. Single
// static binary, pure Go, no CGo. exiftool is required to read RAW files.
//
// The command tree (sort / clean / version) lives in internal/cli; main only
// injects the build version and delegates to cli.Execute.
package main

import (
	"os"

	"github.com/sgaunet/moraine/internal/cli"
)

// version is the build version, overridden with -ldflags "-X main.version=<v>".
var version = "dev"

func main() {
	os.Exit(cli.Execute(version, os.Args[1:], os.Stdout, os.Stderr))
}
