package git

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shutx-net/micawber/core"
)

// TestWhatTheAdapterWritesIsAnOrdinaryGitHistory is the test that says Micawber
// did not invent a private format, which is the whole premise of "Git is the
// source of truth".
//
// Everything is checked with git alone: fsck, the log, the objects, and a clone
// that produces the files a human would expect to find.
func TestWhatTheAdapterWritesIsAnOrdinaryGitHistory(t *testing.T) {
	repo := newTestRepo(t)
	opened := repo.open()

	first, err := opened.Put(t.Context(), newContent(t, "posts/a.md", "a", "\nfirst\n"), testChange("create a"))
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	if _, err := opened.Put(t.Context(), newContent(t, "posts/b.md", "b", "\nb\n"), testChange("create b")); err != nil {
		t.Fatalf("create b: %v", err)
	}
	updated := newContent(t, "posts/a.md", "a", "\nsecond\n")
	updated.Revision = first
	if _, err := opened.Put(t.Context(), updated, testChange("update a")); err != nil {
		t.Fatalf("update a: %v", err)
	}
	if err := opened.Delete(t.Context(), mustPath(t, "posts/b.md"), "", testChange("delete b")); err != nil {
		t.Fatalf("delete b: %v", err)
	}

	repo.fsck()

	want := []string{"delete b", "update a", "create b", "create a"}
	got := strings.Split(strings.TrimSpace(repo.git(nil, nil, "log", "--format=%s", repo.ref)), "\n")
	if len(got) != len(want) {
		t.Fatalf("git log shows %d commits (%v), want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("commit %d is %q, want %q", i, got[i], want[i])
		}
	}
	if stored, want := repo.blob("posts/a.md"), []byte("---\ntitle: a\n---\n\nsecond\n"); !bytes.Equal(stored, want) {
		t.Errorf("git cat-file blob = %q, want %q", stored, want)
	}

	// A clone is what anybody else would do with this repository. Local, so no
	// network and no credentials are involved.
	clone := filepath.Join(t.TempDir(), "clone")
	repo.git(nil, nil, "clone", "-q", "--branch", strings.TrimPrefix(repo.ref, "refs/heads/"), repo.dir, clone)

	checkedOut, err := os.ReadFile(filepath.Join(clone, "posts", "a.md"))
	if err != nil {
		t.Fatalf("read the cloned working tree: %v", err)
	}
	if want := []byte("---\ntitle: a\n---\n\nsecond\n"); !bytes.Equal(checkedOut, want) {
		t.Errorf("the checked-out file is %q, want %q", checkedOut, want)
	}
	if _, err := os.Stat(filepath.Join(clone, "posts", "b.md")); !os.IsNotExist(err) {
		t.Errorf("the deleted document is still in a fresh checkout (%v)", err)
	}
}

// TestCommitsMadeByPlainGitAreVisibleImmediately is what having no cache buys.
//
// Every operation resolves the branch afresh, so there is nothing to invalidate
// and no window in which Micawber disagrees with the repository it is reading.
func TestCommitsMadeByPlainGitAreVisibleImmediately(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"posts/a.md": []byte("---\ntitle: a\n---\n\na\n")})
	opened := repo.open()

	if _, err := opened.Get(t.Context(), mustPath(t, "posts/a.md")); err != nil {
		t.Fatalf("Get: %v", err)
	}

	repo.commit("plain git", map[string][]byte{"posts/c.md": []byte("---\ntitle: c\n---\n\nc\n")})

	entries, err := opened.List(t.Context(), core.Collection{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !containsPath(entries, "posts/c.md") {
		t.Errorf("List = %v, want the document plain git just committed", paths(entries))
	}
	if _, err := opened.Get(t.Context(), mustPath(t, "posts/c.md")); err != nil {
		t.Errorf("Get of the document plain git just committed: %v", err)
	}

	repo.remove("plain git again", "posts/a.md")
	if _, err := opened.Get(t.Context(), mustPath(t, "posts/a.md")); err == nil {
		t.Error("Get still returns a document plain git has removed")
	}
}

// TestAdapterWritesDoNotDisturbUncommittedWork is the honest form of the
// dirty-working-tree question.
//
// The adapter cannot leave a shared checkout looking unmodified: it moves the
// branch under an index nothing has refreshed, so git status reports staged
// modifications afterwards, and this test asserts that rather than a tidiness the
// design does not provide. What it must never do is destroy work, and that is the
// part that matters.
func TestAdapterWritesDoNotDisturbUncommittedWork(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{
		"posts/a.md": []byte("---\ntitle: a\n---\n\na\n"),
		"posts/b.md": []byte("---\ntitle: b\n---\n\nb\n"),
	})
	repo.checkout()

	inProgress := []byte("---\ntitle: a\n---\n\nhalf-written, not committed\n")
	worktreePath := filepath.Join(repo.dir, "posts", "a.md")
	if err := os.WriteFile(worktreePath, inProgress, 0o644); err != nil {
		t.Fatalf("write uncommitted work: %v", err)
	}

	if _, err := repo.open().Put(t.Context(), newContent(t, "posts/c.md", "c", "\nc\n"), testChange("add c")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	after, err := os.ReadFile(worktreePath)
	if err != nil {
		t.Fatalf("read the working tree: %v", err)
	}
	if !bytes.Equal(after, inProgress) {
		t.Errorf("uncommitted work was changed: %q became %q", inProgress, after)
	}

	// And the cost, measured rather than asserted away: the checkout now reports
	// the adapter's own commit as a staged change, because its index predates the
	// branch it is on. This is why Micawber should own its repository.
	status := repo.git(nil, nil, "status", "--short")
	if !strings.Contains(status, "posts/c.md") {
		t.Errorf("git status --short = %q, want the adapter's write to show as a staged change; if this has stopped being true, git/doc.go is now wrong", status)
	}
}

// TestAdapterAndGitAgreeOnEveryRevision checks the two views of the repository
// against each other, entry by entry, so a revision scheme that drifted anywhere
// shows up somewhere.
func TestAdapterAndGitAgreeOnEveryRevision(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{
		"posts/a.md": []byte("---\ntitle: a\n---\n\none\n"),
		"posts/b.md": []byte("---\ntitle: b\n---\n\nb\n"),
	})
	repo.commit("second", map[string][]byte{"posts/a.md": []byte("---\ntitle: a\n---\n\ntwo\n")})
	repo.commit("third", map[string][]byte{"posts/a.md": []byte("---\ntitle: a\n---\n\nthree\n")})
	opened := repo.open()

	entries, err := opened.List(t.Context(), core.Collection{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, entry := range entries {
		if want := core.Revision(repo.blobAt(entry.Path.String())); entry.Revision != want {
			t.Errorf("List says %q for %q, git rev-parse says %q", entry.Revision, entry.Path, want)
		}
	}

	infos, err := opened.History(t.Context(), mustPath(t, "posts/a.md"), 0)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	commits := strings.Split(strings.TrimSpace(repo.git(nil, nil, "log", "--format=%H", repo.ref, "--", "posts/a.md")), "\n")
	if len(infos) != len(commits) {
		t.Fatalf("History has %d entries, git log has %d commits", len(infos), len(commits))
	}
	for i, commit := range commits {
		want := strings.TrimSpace(repo.git(nil, nil, "rev-parse", commit+":posts/a.md"))
		if string(infos[i].Revision) != want {
			t.Errorf("History entry %d is %q, git says %q at commit %s", i, infos[i].Revision, want, commit)
		}
	}
}

// containsPath reports whether a listing holds a given path.
func containsPath(entries []core.ContentEntry, want string) bool {
	for _, entry := range entries {
		if entry.Path.String() == want {
			return true
		}
	}
	return false
}
