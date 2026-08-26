package ui

// SetTerminalCheck replaces the terminal predicate and returns a function restoring
// it, so a test can reach the clauses of Enabled that a pipe short-circuits.
func SetTerminalCheck(f func(any) bool) func() {
	prev := terminalCheck
	terminalCheck = f
	return func() { terminalCheck = prev }
}
