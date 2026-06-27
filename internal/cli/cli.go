// Package cli is moraine's transport layer: it builds the Cobra command tree
// (root + the sort/clean/version subcommands), wires each command to the typed
// config and the app orchestrators, and maps command execution to the process
// exit code. No domain package imports Cobra — the dependency stays contained here.
package cli

import (
	"fmt"
	"io"
)

// Execute builds the command tree, runs it against args (os.Args[1:]), and returns
// the process exit code. It renders all user-facing errors itself (Cobra's own
// error/usage output is silenced) and maps the result with classify:
//
//	nil              → 0 (success; also -h/--help and --version, which print and return nil)
//	runtime failure  → 1 ("error: …")        — validation, exiftool preflight, the run
//	anything else    → 2 ("argument error: …") — unknown command/flag, bad arity/value
func Execute(version string, args []string, stdout, stderr io.Writer) int {
	root := newRootCmd(version, stdout)
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)

	err := root.Execute()
	code := classify(err)
	switch code {
	case exitRuntime:
		_, _ = fmt.Fprintln(stderr, "error:", err)
	case exitUsage:
		_, _ = fmt.Fprintln(stderr, "argument error:", err)
		_, _ = fmt.Fprintln(stderr, "run 'moraine [command] --help' for usage")
	}
	return code
}
