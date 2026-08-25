// Package cli is moraine's transport layer: it builds the Cobra command tree
// (root + the sort/clean/undo/version subcommands), wires each command to the typed
// config and the app orchestrators, and maps command execution to the process
// exit code. No domain package imports Cobra — the dependency stays contained here.
package cli

import (
	"fmt"
	"io"
	"os"
)

// Execute builds the command tree, runs it against args (os.Args[1:]), and returns
// the process exit code. It renders all user-facing errors itself (Cobra's own
// error/usage output is silenced) and maps the result with classify:
//
//	nil              → 0 (success; also -h/--help and --version, which print and return nil)
//	runtime failure  → 1 ("error: …")        — validation, exiftool preflight, the run
//	anything else    → 2 ("argument error: …") — unknown command/flag, bad arity/value
//
// stdout receives run results only (see output.go for that contract); every log,
// error and progress line goes to stderr, so moraine is safe in a pipe
// (Constitution Principle V).
func Execute(version string, args []string, stdout, stderr io.Writer) int {
	return execute(version, args, os.Stdin, stdout, stderr)
}

// execute is Execute with standard input as a parameter. Only `config edit` reads it,
// and only to ask questions; every other command reads files and flags. Keeping it a
// parameter rather than a package variable is what lets a test drive the form without
// a terminal and without shared state (see export_test.go).
func execute(version string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	root := newRootCmd(version, stdout, stderr)
	root.SetArgs(args)
	root.SetIn(stdin)
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
