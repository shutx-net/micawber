package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/shutx-net/micawber/core"
)

// TestRunnerRejectsAnArgumentThatLooksLikeAnOption pins the leading-dash rule at
// the argv boundary, which is the only place it belongs.
//
// Every string below is one core.NewContentPath accepts, and correctly so: none
// of them traverses anywhere and all of them are legal filenames on POSIX and
// legal paths in Git. They are dangerous only to a subprocess route, so the
// check lives here rather than in core, where it would make one implementation's
// hazard every other implementation's rule.
func TestRunnerRejectsAnArgumentThatLooksLikeAnOption(t *testing.T) {
	// A binary that cannot exist, so a started process is distinguishable from a
	// rejected argument: if the check ever stops firing, the error changes.
	r := runner{bin: filepath.Join(t.TempDir(), "no-such-git"), dir: t.TempDir()}

	// core accepts every one of these, which is the point: they are the exposure
	// this check exists to close, so the test asserts that too.
	for _, operand := range []string{"-oevil.md", "--upload-pack=x.md", "-", "-rf.md", "--help"} {
		t.Run(operand, func(t *testing.T) {
			if _, err := core.NewContentPath(operand); err != nil {
				t.Fatalf("core.NewContentPath(%q) = %v; core now rejects it, so this test no longer records the exposure it was written for", operand, err)
			}

			_, err := r.run(t.Context(), invocation{args: []string{"ls-tree", "-r", "-z", "HEAD", "--", operand}})
			if !errors.Is(err, core.ErrInvalid) {
				t.Fatalf("run with operand %q: got %v, want an error matching core.ErrInvalid", operand, err)
			}
			if strings.Contains(err.Error(), "no-such-git") {
				t.Errorf("run with operand %q started a process: %v", operand, err)
			}
		})
	}
}

// TestRunnerAcceptsOrdinaryOperands is the other half of the rule: it must reject
// a leading dash and nothing else, so a path with a dash anywhere but the front
// still works.
func TestRunnerAcceptsOrdinaryOperands(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"posts/a-b.md": []byte("hello\n"), "posts/x/-y.md": []byte("hi\n")})

	r := runner{bin: "git", dir: repo.dir}
	for _, operand := range []string{"posts/a-b.md", "posts/x/-y.md", "posts"} {
		if _, err := r.run(t.Context(), invocation{args: []string{"ls-tree", "-r", "-z", repo.ref, "--", operand}}); err != nil {
			t.Errorf("run with operand %q: %v", operand, err)
		}
	}
}

// TestRunnerPutsGitStderrInTheError keeps git's own diagnosis attached to the
// failure. Classification never reads it -- git's messages are localised and are
// not a compatibility promise -- but a human reading a log needs it.
func TestRunnerPutsGitStderrInTheError(t *testing.T) {
	repo := newTestRepo(t)
	r := runner{bin: "git", dir: repo.dir}

	_, err := r.run(t.Context(), invocation{args: []string{"cat-file", "-t", "0000000000000000000000000000000000000000"}})
	if err == nil {
		t.Fatal("run of a failing command: got nil error")
	}

	var gitErr *gitError
	if !errors.As(err, &gitErr) {
		t.Fatalf("run: got %T (%v), want a *gitError", err, err)
	}
	if gitErr.code == 0 {
		t.Errorf("gitError.code = 0, want the non-zero exit status")
	}
	if gitErr.stderr == "" {
		t.Errorf("gitError.stderr is empty; git's diagnosis was dropped")
	}
	if !strings.Contains(err.Error(), "cat-file") {
		t.Errorf("Error() = %q, want the failed argument vector in it", err.Error())
	}
}

// TestRunnerHonoursContextCancellation covers both halves of the cancellation
// contract: the caller gets ctx.Err(), and the child is asked to stop with
// SIGTERM rather than SIGKILL.
//
// The signal is the part that matters operationally. git removes its <ref>.lock
// through a signal handler, and a handler does not run for SIGKILL, so a
// cancelled write under exec.CommandContext's default would leave a lock that
// blocks every later writer of the repository -- including the user's own git.
func TestRunnerHonoursContextCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX signals; the adapter's cancellation behaviour is not specified for Windows")
	}
	if gitAbsent != "" {
		t.Skip(gitAbsent)
	}

	dir := t.TempDir()
	witness := filepath.Join(dir, "sigterm")
	script := filepath.Join(dir, "slow-git")
	body := "#!/bin/sh\ntrap 'printf caught > " + witness + "; exit 143' TERM\nsleep 30 &\nwait\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write stand-in git: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := runner{bin: script, dir: dir}.run(ctx, invocation{args: []string{"rev-parse"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run of a cancelled command: got %v, want an error matching context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("run returned after %v; cancellation did not stop the child promptly", elapsed)
	}

	// The handler writes the witness before exiting, so its presence is proof the
	// child was signalled rather than killed.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(witness); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the child was not sent SIGTERM; git would not have cleaned up its ref lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestRunnerEnvironmentIgnoresUserAndSystemConfig checks the two properties every
// other test in this package rests on.
//
// The first is hermeticity: the runner builds its environment from this
// process's, and TestMain has scrubbed that, so a developer's ~/.gitconfig
// reaches neither a fixture nor the adapter. Writing one and finding it unread is
// what makes that concrete rather than assumed.
//
// The second is that the runner drops the variables that would redirect a
// command away from its own repository. Micawber may run somewhere that already
// has GIT_DIR set -- inside a hook, say -- and inheriting it would send every
// write to another repository entirely.
func TestRunnerEnvironmentIgnoresUserAndSystemConfig(t *testing.T) {
	repo := newTestRepo(t)

	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), ".gitconfig"), []byte("[user]\n\tname = Should Not Be Seen\n"), 0o600); err != nil {
		t.Fatalf("write a global config: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(filepath.Join(os.Getenv("HOME"), ".gitconfig")) })

	r := runner{bin: "git", dir: repo.dir}
	out, err := r.run(t.Context(), invocation{args: []string{"config", "--get", "user.name"}})
	if err == nil {
		t.Errorf("git config --get user.name returned %q; the global config was read", strings.TrimSpace(string(out)))
	}

	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "elsewhere.git"))
	got, err := r.run(t.Context(), invocation{args: []string{"rev-parse", "--absolute-git-dir"}})
	if err != nil {
		t.Fatalf("rev-parse --absolute-git-dir with GIT_DIR set: %v", err)
	}
	want, err := filepath.EvalSymlinks(filepath.Join(repo.dir, ".git"))
	if err != nil {
		t.Fatalf("resolve the fixture's git dir: %v", err)
	}
	if resolved, err := filepath.EvalSymlinks(strings.TrimSpace(string(got))); err != nil || resolved != want {
		t.Errorf("git dir = %q, want %q; an inherited GIT_DIR redirected the command", strings.TrimSpace(string(got)), want)
	}
}
