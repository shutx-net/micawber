package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/shutx-net/micawber/core"
)

// cancelGrace is how long a cancelled git is given to exit after SIGTERM before
// the runtime escalates to SIGKILL. It only has to cover removing a lock file
// and unwinding, so it is short enough that a wedged child cannot hold a request
// open and long enough that a busy machine does not lose the cleanup.
const cancelGrace = 5 * time.Second

// maxStderrBytes caps how much of git's diagnosis an error carries. A truncated
// message is still enough to diagnose with, and an unbounded one turns a failure
// into a memory cost.
const maxStderrBytes = 4 << 10

// operandSeparator is the "--" every git command puts between its own options
// and the paths that follow. Everything after it is caller-derived, which is why
// it is also where the runner applies safeArg.
const operandSeparator = "--"

// redirectingEnv names the environment variables that would send a git command
// somewhere other than the repository the adapter was opened on, or would put an
// authorship on a commit that the caller did not ask for.
//
// They are dropped rather than trusted because Micawber may well run somewhere
// that already sets them -- inside a Git hook, most obviously -- and inheriting
// GIT_DIR would quietly write every document into another repository. The
// adapter supplies its own GIT_INDEX_FILE and GIT_AUTHOR_*/GIT_COMMITTER_* per
// invocation, so nothing here is lost by dropping the inherited ones.
var redirectingEnv = []string{
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_COMMON_DIR",
	"GIT_INDEX_FILE",
	"GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_NAMESPACE",
	"GIT_AUTHOR_NAME",
	"GIT_AUTHOR_EMAIL",
	"GIT_AUTHOR_DATE",
	"GIT_COMMITTER_NAME",
	"GIT_COMMITTER_EMAIL",
	"GIT_COMMITTER_DATE",
}

// fixedEnv is set on every invocation. The adapter never talks to a remote, so
// nothing it runs has a reason to ask a human for anything; a git that could
// prompt would hang a server rather than fail it.
//
// Nothing here disables the user's own configuration. Micawber drives the user's
// git deliberately, and a future remote phase depends on their credential helper
// and ssh configuration still being in effect.
var fixedEnv = []string{"GIT_TERMINAL_PROMPT=0"}

// runner runs one git command against one repository directory. It holds no
// mutable state, so it is safe to share and cheap to copy.
//
// Every subprocess in Micawber goes through this type, and this is the only file
// that imports os/exec: TestOSExecIsConfinedToTheRunner keeps it that way, so an
// auditor checking what the adapter can execute reads one file.
type runner struct {
	// bin is the git executable, already resolved to a path.
	bin string
	// dir is the repository the command runs in.
	dir string
	// observe, when not nil, is called with each argument vector just before it
	// runs. It is the seam this package's own tests use to assert what was
	// actually executed, and to inject an interleaving -- cancelling at a chosen
	// step -- that a race could not reproduce. Nothing sets it in production.
	observe func(args []string)
}

// invocation is one git command: its argument vector, the bytes to feed it on
// standard input, and the environment entries to add to the adapter's own.
//
// Data travels on stdin wherever a plumbing command accepts it there, which is
// most of them, so a document's bytes, its path and a commit message are never
// arguments at all.
type invocation struct {
	// args is the argument vector after the program name.
	args []string
	// stdin is fed to the command and closed; nil means no input.
	stdin []byte
	// env holds "NAME=value" entries added to the adapter's environment.
	env []string
}

// run executes in and returns its standard output.
//
// A non-zero exit becomes a *gitError carrying the argument vector, the status
// and git's standard error. A cancelled context becomes ctx.Err(): the child is
// sent SIGTERM rather than the runtime's default SIGKILL, so git's own handler
// runs and removes any lock file it holds. That is the difference between a
// cancelled request and a repository no later writer can lock.
func (r runner) run(ctx context.Context, in invocation) ([]byte, error) {
	if err := checkOperands(in.args); err != nil {
		return nil, err
	}
	if r.observe != nil {
		r.observe(in.args)
	}

	cmd := exec.CommandContext(ctx, r.bin, in.args...)
	cmd.Dir = r.dir
	cmd.Env = r.environ(in.env)
	if in.stdin != nil {
		cmd.Stdin = bytes.NewReader(in.stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = cancelGrace

	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), nil
	}
	// Cancellation first: a git stopped by its signal also exits non-zero, and
	// reporting that as a git failure would hide why it stopped.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("git %s: %w", subcommand(in.args), ctxErr)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil, &gitError{
			args:   slices.Clone(in.args),
			code:   exitErr.ExitCode(),
			stderr: trimStderr(stderr.Bytes()),
		}
	}
	return nil, fmt.Errorf("git %s: %w", subcommand(in.args), err)
}

// environ builds the environment for one invocation: this process's, less the
// variables that would redirect the command or forge an identity, plus the
// adapter's fixed entries and the caller's extra ones.
//
// Starting from os.Environ() rather than from nothing is deliberate. The user's
// PATH, HOME and git configuration are what make this the user's git rather than
// a sandboxed one, which is the whole premise of driving the binary.
func (r runner) environ(extra []string) []string {
	base := os.Environ()
	env := make([]string, 0, len(base)+len(fixedEnv)+len(extra))
	for _, entry := range base {
		name, _, _ := strings.Cut(entry, "=")
		if slices.Contains(redirectingEnv, name) {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, fixedEnv...)
	return append(env, extra...)
}

// checkOperands applies safeArg to every argument after the "--" separator.
//
// Everything before the separator is a literal this package wrote, an object id
// git itself printed, or the branch ref Open validated. Everything after it came
// from a caller, so it is checked here, once, at the boundary where the hazard
// exists -- rather than at each of the call sites that would have to remember.
func checkOperands(args []string) error {
	separator := slices.Index(args, operandSeparator)
	if separator < 0 {
		return nil
	}
	for _, arg := range args[separator+1:] {
		if err := safeArg(arg); err != nil {
			return err
		}
	}
	return nil
}

// safeArg reports whether s can be passed to git as an operand without being
// read as an option: it may not begin with a dash.
//
// The rule is one sentence on purpose. Everything else a path could carry is
// already impossible by the time it reaches here -- core rejects control
// characters, backslashes, empty and ".." segments and a leading-colon first
// segment, which is what makes Git's pathspec magic unreachable too -- so a
// second, overlapping validator here would be this adapter's hazards leaking
// back into the shared vocabulary.
//
// The returned error unwraps to core.ErrInvalid.
func safeArg(s string) error {
	if strings.HasPrefix(s, "-") {
		return &core.ValidationError{
			Kind:   "git operand",
			Value:  s,
			Reason: "begins with a dash, so git would read it as an option",
		}
	}
	return nil
}

// lookGit resolves the git executable, so that a missing binary is reported once
// at Open rather than as a puzzling failure on the first read.
func lookGit(bin string) (string, error) {
	path, err := exec.LookPath(bin)
	if err != nil {
		return "", sentinelf(core.ErrInvalid, err, "git binary %q is not usable", bin)
	}
	return path, nil
}

// subcommand returns the first argument, for error messages that name what was
// being run without repeating the whole vector.
func subcommand(args []string) string {
	if len(args) == 0 {
		return "(no arguments)"
	}
	return args[0]
}

// trimStderr bounds and tidies git's diagnosis for an error message.
func trimStderr(b []byte) string {
	if len(b) > maxStderrBytes {
		b = b[:maxStderrBytes]
	}
	return strings.TrimSpace(string(b))
}
