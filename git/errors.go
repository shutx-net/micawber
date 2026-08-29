package git

import (
	"fmt"
	"strings"
)

// gitError is a git command that exited non-zero: what was run, how it ended,
// and what it said.
//
// It carries the argument vector and standard error for a human to diagnose
// with, and neither is ever consulted to decide which of core's sentinels a
// failure maps to. git's messages are translated into the user's locale and are
// not a compatibility promise, so classification reads the structured signals
// plumbing gives instead -- a "missing" line from cat-file, an object type, an
// exit status -- and never prose.
//
// It never carries the bytes that were fed to the command. A front-matter block
// may hold an API token, which is the rule markdown states for its own errors,
// and the environment is where a credential helper would appear.
type gitError struct {
	// args is the argument vector git was given, after the program name.
	args []string
	// code is the process exit status.
	code int
	// stderr is what git wrote to standard error, trimmed and bounded.
	stderr string
}

// Error names the command, its exit status and git's own message.
func (e *gitError) Error() string {
	msg := fmt.Sprintf("git %s: exit status %d", strings.Join(e.args, " "), e.code)
	if e.stderr != "" {
		msg += ": " + e.stderr
	}
	return msg
}

// sentinelf wraps cause in a message and one of core's sentinels, so that the
// result matches errors.Is against both: the sentinel an HTTP layer maps to a
// status code, and the underlying failure a human needs in order to fix it.
//
// cause may be nil, for a refusal the adapter made without running anything.
func sentinelf(sentinel, cause error, format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	if cause == nil {
		return fmt.Errorf("git: %s: %w", msg, sentinel)
	}
	return fmt.Errorf("git: %s: %w: %w", msg, sentinel, cause)
}
