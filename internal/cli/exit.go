package cli

import "errors"

// Exit codes follow the CLI contract: 0 success, 1 runtime error, 2 usage error.
const (
	exitOK      = 0
	exitRuntime = 1
	exitUsage   = 2
)

// runtimeError marks an error as a runtime failure (exit 1) rather than a usage
// error (exit 2). The CLI wraps post-parse failures (filesystem validation, the
// exiftool preflight, the organize/clean run) with asRuntime; everything else that
// surfaces from command execution — flag-parse errors, unknown commands, wrong
// argument counts, invalid flag values, and the config constructors' cross-field
// checks — is left unwrapped and classified as a usage error.
type runtimeError struct{ err error }

func (e *runtimeError) Error() string { return e.err.Error() }
func (e *runtimeError) Unwrap() error { return e.err }

// asRuntime tags err as a runtime (exit 1) failure. It returns nil for a nil error
// so it can wrap a call's result directly (e.g. return asRuntime(cfg.Validate())).
func asRuntime(err error) error {
	if err == nil {
		return nil
	}
	return &runtimeError{err: err}
}

// classify maps the error returned by command execution to an exit code:
// nil → success, a runtimeError → runtime, anything else → usage.
func classify(err error) int {
	switch {
	case err == nil:
		return exitOK
	case errors.As(err, new(*runtimeError)):
		return exitRuntime
	default:
		return exitUsage
	}
}
