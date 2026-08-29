package git

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shutx-net/micawber/core"
)

// TestDeleteRemovesTheDocumentAndCommits is the operation working normally: the
// document goes, and the removal is a commit like any other change.
func TestDeleteRemovesTheDocumentAndCommits(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{
		"posts/a.md": []byte("---\ntitle: a\n---\n\na\n"),
		"posts/b.md": []byte("---\ntitle: b\n---\n\nb\n"),
	})
	opened := repo.open()

	if err := opened.Delete(t.Context(), mustPath(t, "posts/a.md"), "", testChange("remove a")); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := repo.commitCount(); got != 2 {
		t.Errorf("commit count = %d, want 2", got)
	}
	if _, err := opened.Get(t.Context(), mustPath(t, "posts/a.md")); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Get after Delete: got %v, want an error matching core.ErrNotFound", err)
	}
	if _, err := opened.Get(t.Context(), mustPath(t, "posts/b.md")); err != nil {
		t.Errorf("Get of the document that was not deleted: %v", err)
	}
}

// TestDeleteWithAZeroRevisionIsUnconditional is half of the asymmetry core
// argues for: an unconditional delete must not become conditional by accident.
func TestDeleteWithAZeroRevisionIsUnconditional(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"posts/a.md": []byte("---\ntitle: a\n---\n\na\n")})
	opened := repo.open()

	if _, err := opened.Get(t.Context(), mustPath(t, "posts/a.md")); err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Somebody else rewrites it between the read and the delete. A zero revision
	// says "whatever revision it has", so this must still succeed.
	repo.commit("somebody else", map[string][]byte{"posts/a.md": []byte("---\ntitle: a\n---\n\ntheirs\n")})

	if err := opened.Delete(t.Context(), mustPath(t, "posts/a.md"), "", testChange("remove a")); err != nil {
		t.Fatalf("unconditional Delete after the document changed: %v", err)
	}
}

// TestDeleteWithAMatchingRevisionSucceeds is the conditional delete when the
// condition holds.
func TestDeleteWithAMatchingRevisionSucceeds(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"posts/a.md": []byte("---\ntitle: a\n---\n\na\n")})
	opened := repo.open()

	got, err := opened.Get(t.Context(), mustPath(t, "posts/a.md"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := opened.Delete(t.Context(), got.Path, got.Revision, testChange("remove a")); err != nil {
		t.Fatalf("Delete with a matching revision: %v", err)
	}
	if _, err := opened.Get(t.Context(), got.Path); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Get after Delete: got %v, want an error matching core.ErrNotFound", err)
	}
}

// TestDeleteWithAStaleRevisionIsErrConflict is the other half: a non-empty
// revision deletes only while it still matches.
func TestDeleteWithAStaleRevisionIsErrConflict(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"posts/a.md": []byte("---\ntitle: a\n---\n\na\n")})
	opened := repo.open()

	stale, err := opened.Get(t.Context(), mustPath(t, "posts/a.md"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	repo.commit("somebody else", map[string][]byte{"posts/a.md": []byte("---\ntitle: a\n---\n\ntheirs\n")})
	before := repo.head()

	if err := opened.Delete(t.Context(), stale.Path, stale.Revision, testChange("remove a")); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("Delete with a stale revision: got %v, want an error matching core.ErrConflict", err)
	}
	if repo.head() != before {
		t.Error("the branch moved for a refused delete")
	}
}

// TestDeleteOfAMissingDocumentIsErrNotFound and its zero-revision sibling below
// are written as a pair, because the boundary between them is the thing that is
// easy to get wrong.
func TestDeleteOfAMissingDocumentIsErrNotFound(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"posts/a.md": []byte("---\ntitle: a\n---\n\na\n")})

	err := repo.open().Delete(t.Context(), mustPath(t, "posts/nope.md"), "deadbeef", testChange("remove"))
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Delete of a missing document: got %v, want an error matching core.ErrNotFound", err)
	}
}

// TestDeleteOfAMissingDocumentWithAZeroRevisionIsAlsoErrNotFound is where an
// implementer is most likely to be helpful and wrong.
//
// Unconditional means "whatever revision it has", not "even if it is not there".
// core is explicit that deleting something absent is an error rather than a
// silent success: the repository reports what it observed, and a caller that
// wants idempotence tests for it.
func TestDeleteOfAMissingDocumentWithAZeroRevisionIsAlsoErrNotFound(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"posts/a.md": []byte("---\ntitle: a\n---\n\na\n")})
	before := repo.head()

	err := repo.open().Delete(t.Context(), mustPath(t, "posts/nope.md"), "", testChange("remove"))
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("unconditional Delete of a missing document: got %v, want an error matching core.ErrNotFound", err)
	}
	if repo.head() != before {
		t.Error("the branch moved for a delete of something that was not there")
	}
}

// TestDeleteRemovesTheParentTreeWhenItBecomesEmpty records something Git gives
// for free and a filesystem adapter would have to work for: there are no empty
// trees, so the directory goes when its last document does.
func TestDeleteRemovesTheParentTreeWhenItBecomesEmpty(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{
		"posts/nested/only.md": []byte("---\ntitle: o\n---\n\no\n"),
		"posts/a.md":           []byte("---\ntitle: a\n---\n\na\n"),
	})

	if err := repo.open().Delete(t.Context(), mustPath(t, "posts/nested/only.md"), "", testChange("remove it")); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := strings.TrimSpace(repo.git(nil, nil, "ls-tree", "-r", repo.ref, "--", "posts/nested")); got != "" {
		t.Errorf("git ls-tree posts/nested = %q, want nothing", got)
	}
	if repo.blobAt("posts/a.md") == "" {
		t.Error("the sibling document is gone")
	}
}

// TestDeleteRecordsTheChangeAsTheCommit is the core.Change contract again: a
// removal is a change like any other and carries the same authorship.
func TestDeleteRecordsTheChangeAsTheCommit(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"posts/a.md": []byte("---\ntitle: a\n---\n\na\n")})
	change := core.Change{
		Message: "Retire the post\n\nIt was wrong.\n",
		Author:  core.Author{Name: "Emma Micawber", Email: "emma@example.invalid"},
		Time:    fixtureTime(),
	}

	if err := repo.open().Delete(t.Context(), mustPath(t, "posts/a.md"), "", change); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := repo.commitField("%an"); got != change.Author.Name {
		t.Errorf("commit author = %q, want %q", got, change.Author.Name)
	}
	if got := repo.commitField("%B"); strings.TrimRight(got, "\n") != strings.TrimRight(change.Message, "\n") {
		t.Errorf("commit message = %q, want %q", got, change.Message)
	}
}

// TestDeleteOfADamagedDocumentSucceeds is how a bad merge gets cleaned up.
//
// Delete never parses -- it needs the blob id for the compare-and-swap and
// nothing else -- which is what makes it the repair tool for a document Get
// refuses to return.
func TestDeleteOfADamagedDocumentSucceeds(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"posts/broken.md": []byte("---\ntitle: a\nnever terminated\n")})
	opened := repo.open()

	if _, err := opened.Get(t.Context(), mustPath(t, "posts/broken.md")); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Get of the damaged document: got %v, want core.ErrInvalid; the premise of this test has changed", err)
	}
	if err := opened.Delete(t.Context(), mustPath(t, "posts/broken.md"), "", testChange("clean up a bad merge")); err != nil {
		t.Fatalf("Delete of a damaged document: %v", err)
	}

	entries, err := opened.List(t.Context(), core.Collection{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("List = %v, want nothing left", paths(entries))
	}
}

// TestDeleteOnAnUnbornBranchIsErrNotFound is the same rule where there is not
// even a tree to look in.
func TestDeleteOnAnUnbornBranchIsErrNotFound(t *testing.T) {
	repo := newTestRepo(t)

	err := repo.open().Delete(t.Context(), mustPath(t, "posts/a.md"), "", testChange("remove"))
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Delete on an unborn branch: got %v, want an error matching core.ErrNotFound", err)
	}
	if _, born := repo.tip(); born {
		t.Error("a refused delete created the first commit")
	}
}

// TestDeleteHonoursContentRootAndContextCancellation covers the two obligations
// every operation shares.
func TestDeleteHonoursContentRootAndContextCancellation(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{
		"content/posts/a.md": []byte("---\ntitle: a\n---\n\na\n"),
		"secret/b.md":        []byte("---\ntitle: b\n---\n\nb\n"),
	})
	opened := repo.open(WithContentRoot(mustCollection(t, "content")))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := opened.Delete(ctx, mustPath(t, "posts/a.md"), "", testChange("remove")); !errors.Is(err, context.Canceled) {
		t.Errorf("Delete with a cancelled context: got %v, want an error matching context.Canceled", err)
	}

	// A path outside the root is not addressable, so it is not there.
	if err := opened.Delete(t.Context(), mustPath(t, "secret/b.md"), "", testChange("remove")); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Delete of a path outside the content root: got %v, want an error matching core.ErrNotFound", err)
	}
	if repo.blobAt("secret/b.md") == "" {
		t.Error("the document outside the content root was removed")
	}

	if err := opened.Delete(t.Context(), mustPath(t, "posts/a.md"), "", testChange("remove a")); err != nil {
		t.Fatalf("Delete within the content root: %v", err)
	}
	if _, err := repo.tryGit(nil, nil, "rev-parse", repo.ref+":content/posts/a.md"); err == nil {
		t.Error("the document within the content root is still there")
	}
}

// TestDeleteRejectsAnInvalidChange leaves the rules where core wrote them, the
// same way Put does. A removal is still a commit, and a commit still needs a
// message and an author.
func TestDeleteRejectsAnInvalidChange(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"posts/a.md": []byte("---\ntitle: a\n---\n\na\n")})
	opened := repo.open()
	before := repo.head()

	cases := map[string]core.Change{
		"empty message": {Author: core.Author{Name: "A"}},
		"no author":     {Message: "remove it"},
		"blank author":  {Message: "remove it", Author: core.Author{Name: "  "}},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			if err := opened.Delete(t.Context(), mustPath(t, "posts/a.md"), "", change); !errors.Is(err, core.ErrInvalid) {
				t.Errorf("Delete: got %v, want an error matching core.ErrInvalid", err)
			}
		})
	}
	if repo.head() != before {
		t.Error("a refused delete moved the branch")
	}
	if _, err := opened.Get(t.Context(), mustPath(t, "posts/a.md")); err != nil {
		t.Errorf("the document was removed by a refused delete: %v", err)
	}
}
