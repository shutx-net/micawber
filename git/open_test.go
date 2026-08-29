package git

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shutx-net/micawber/core"
)

// TestRepositorySatisfiesTheCoreInterfaces is the compile-time pair of
// assertions written as a test as well, so that the intent is greppable and a
// reader looking for "what does this implement" finds a named answer.
func TestRepositorySatisfiesTheCoreInterfaces(t *testing.T) {
	var repo any = (*Repository)(nil)

	if _, ok := repo.(core.ContentRepository); !ok {
		t.Errorf("*Repository does not implement core.ContentRepository")
	}
	if _, ok := repo.(core.ContentHistory); !ok {
		t.Errorf("*Repository does not implement core.ContentHistory")
	}
}

// TestOpenReadsTheCheckedOutBranch is the default the adapter is meant to have:
// point it at a repository and it writes where the repository is already
// pointing.
func TestOpenReadsTheCheckedOutBranch(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"a.md": []byte("hi\n")})

	if got := repo.open().ref; got != "refs/heads/main" {
		t.Errorf("ref = %q, want %q", got, "refs/heads/main")
	}

	repo.git(nil, nil, "branch", "other")
	repo.git(nil, nil, "symbolic-ref", "HEAD", "refs/heads/other")
	if got := repo.open().ref; got != "refs/heads/other" {
		t.Errorf("ref = %q, want %q", got, "refs/heads/other")
	}
}

// TestOpenHonoursWithBranch covers naming a branch other than the checked-out
// one, in both the short and the fully qualified spelling.
func TestOpenHonoursWithBranch(t *testing.T) {
	repo := newTestRepo(t)

	for _, given := range []string{"drafts", "refs/heads/drafts"} {
		if got := repo.open(WithBranch(given)).ref; got != "refs/heads/drafts" {
			t.Errorf("WithBranch(%q): ref = %q, want %q", given, got, "refs/heads/drafts")
		}
	}
}

// TestOpenAcceptsAnUnbornBranch keeps Micawber able to bootstrap a repository the
// operator has only just run git init in. Refusing here would mean the first
// thing a new user does produces an error.
func TestOpenAcceptsAnUnbornBranch(t *testing.T) {
	repo := newTestRepo(t)

	opened := repo.open()
	if opened.ref != "refs/heads/main" {
		t.Errorf("ref = %q, want %q", opened.ref, "refs/heads/main")
	}
}

// TestOpenRejectsADetachedHEAD fails at startup rather than at the first write.
//
// A compare-and-swap needs a name to swap on, and a detached HEAD has none:
// committing against it would produce commits reachable from nothing, which is
// total data loss wearing the shape of success. Refusing at Open also stops Get
// and List from appearing to work against a repository nothing can be written
// to.
func TestOpenRejectsADetachedHEAD(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"a.md": []byte("hi\n")})
	repo.git(nil, nil, "checkout", "-q", "--detach")

	_, err := Open(t.Context(), repo.dir)
	if !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Open on a detached HEAD: got %v, want an error matching core.ErrInvalid", err)
	}

	// Naming a branch is the way out, so a caller who detached on purpose is not
	// locked out of their own repository.
	if got := repo.open(WithBranch("main")).ref; got != "refs/heads/main" {
		t.Errorf("WithBranch on a detached HEAD: ref = %q, want %q", got, "refs/heads/main")
	}
}

// TestOpenRejectsADirectoryThatIsNotARepository reports a misconfiguration as
// one, rather than failing later with something about an object id.
func TestOpenRejectsADirectoryThatIsNotARepository(t *testing.T) {
	if gitAbsent != "" {
		t.Skip(gitAbsent)
	}

	_, err := Open(t.Context(), t.TempDir())
	if !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Open of a plain directory: got %v, want an error matching core.ErrInvalid", err)
	}

	_, err = Open(t.Context(), filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("Open of a missing directory: got nil error")
	}
}

// TestOpenReportsAMissingGitBinary is the failure a contributor without git will
// actually see, so it should say what is wrong in one line.
func TestOpenReportsAMissingGitBinary(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "definitely-not-git")

	_, err := Open(t.Context(), t.TempDir(), WithGitBinary(missing))
	if err == nil {
		t.Fatal("Open with a git binary that does not exist: got nil error")
	}
	if !errors.Is(err, core.ErrInvalid) {
		t.Errorf("Open with a missing git binary: got %v, want an error matching core.ErrInvalid", err)
	}
	if got := err.Error(); !strings.Contains(got, "definitely-not-git") {
		t.Errorf("Error() = %q, want the binary it looked for named in it", got)
	}
}

// TestOpenValidatesTheContentRoot closes the one place a caller-supplied
// directory reaches an argument vector.
//
// core.NewCollection accepts "-x": it is a legal directory name and not a
// traversal, so core is right to allow it. It is hazardous only to a subprocess
// route, so the adapter is where it has to be refused, and Open is where the
// caller finds out.
func TestOpenValidatesTheContentRoot(t *testing.T) {
	repo := newTestRepo(t)

	root, err := core.NewCollection("-x")
	if err != nil {
		t.Fatalf("core.NewCollection(%q) = %v; core now rejects it, so this test no longer records the exposure it was written for", "-x", err)
	}

	if _, err := Open(t.Context(), repo.dir, WithContentRoot(root)); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Open with a content root that looks like an option: got %v, want an error matching core.ErrInvalid", err)
	}

	ordinary := mustCollection(t, "content")
	if got := repo.open(WithContentRoot(ordinary)).root; got != ordinary {
		t.Errorf("root = %v, want %v", got, ordinary)
	}
}

// TestOpenRejectsABranchThatLooksLikeAnOption is the same rule for the other
// caller-supplied string that reaches argv.
func TestOpenRejectsABranchThatLooksLikeAnOption(t *testing.T) {
	repo := newTestRepo(t)

	for _, branch := range []string{"--upload-pack=x", "-x"} {
		if _, err := Open(t.Context(), repo.dir, WithBranch(branch)); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("WithBranch(%q): got %v, want an error matching core.ErrInvalid", branch, err)
		}
	}
	if _, err := Open(t.Context(), repo.dir, WithBranch("")); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("WithBranch(%q): got %v, want an error matching core.ErrInvalid", "", err)
	}
}
