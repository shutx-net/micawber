package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shutx-net/micawber/core"
)

// newContent is a document assembled by hand, as a caller of the HTTP layer will
// eventually hand one over: no Layout, and no revision unless the test sets one.
func newContent(t *testing.T, path, title, body string) core.Content {
	t.Helper()
	return core.Content{
		Path: mustPath(t, path),
		FrontMatter: core.FrontMatter{
			Format: core.FrontMatterYAML,
			Fields: map[string]any{"title": title},
		},
		Body: []byte(body),
	}
}

// commitField reads one field of the branch tip with plain git, so that what the
// adapter wrote is checked by something other than the adapter.
func (r *testRepo) commitField(format string) string {
	r.t.Helper()
	return strings.TrimRight(r.git(nil, nil, "log", "-1", "--format="+format, r.ref), "\n")
}

// TestPutCreatesADocumentAndReturnsItsBlobRevision is the create path end to
// end: a commit appears, the document is in it, and the revision that comes back
// is the blob's object id.
func TestPutCreatesADocumentAndReturnsItsBlobRevision(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"posts/existing.md": []byte("---\ntitle: e\n---\n\ne\n")})
	opened := repo.open()
	before := repo.head()

	rev, err := opened.Put(t.Context(), newContent(t, "posts/new.md", "new", "\nhello\n"), testChange("add new"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	if repo.head() == before {
		t.Fatal("the branch did not move")
	}
	if want := core.Revision(repo.blobAt("posts/new.md")); rev != want {
		t.Errorf("Put returned %q, want the blob object id %q", rev, want)
	}
	if got, want := repo.blob("posts/new.md"), []byte("---\ntitle: new\n---\n\nhello\n"); !bytes.Equal(got, want) {
		t.Errorf("stored bytes = %q, want %q", got, want)
	}
	// The document that was already there is still there: a write rebuilds the
	// tree from the parent rather than replacing it.
	if repo.blobAt("posts/existing.md") == "" {
		t.Error("the document that was already committed is gone")
	}

	got, err := opened.Get(t.Context(), mustPath(t, "posts/new.md"))
	if err != nil {
		t.Fatalf("Get after Put: %v", err)
	}
	if got.Revision != rev {
		t.Errorf("Get says %q, Put said %q", got.Revision, rev)
	}
}

// TestPutCreateAtAnOccupiedPathIsErrExists is the create half of the
// compare-and-swap: a zero revision means "there must be nothing here".
func TestPutCreateAtAnOccupiedPathIsErrExists(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"posts/a.md": []byte("---\ntitle: a\n---\n\na\n")})
	before := repo.head()

	_, err := repo.open().Put(t.Context(), newContent(t, "posts/a.md", "again", "\nagain\n"), testChange("clobber"))
	if !errors.Is(err, core.ErrExists) {
		t.Fatalf("Put creating over an existing document: got %v, want an error matching core.ErrExists", err)
	}
	if repo.head() != before {
		t.Error("the branch moved for a refused write")
	}
}

// TestPutCreateUsesTheCanonicalLayout states what a document with no previous
// shape is written as: markdown's canonical delimiters, since there is nothing
// to preserve.
func TestPutCreateUsesTheCanonicalLayout(t *testing.T) {
	repo := newTestRepo(t)

	if _, err := repo.open().Put(t.Context(), newContent(t, "a.md", "new", "\nhello\n"), testChange("create")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got, want := repo.blob("a.md"), []byte("---\ntitle: new\n---\n\nhello\n"); !bytes.Equal(got, want) {
		t.Errorf("stored bytes = %q, want %q", got, want)
	}
}

// TestPutRecordsTheChangeAsTheCommit is core.Change made concrete: the message,
// the author and the time on the call are the message, the author and the time
// on the commit.
func TestPutRecordsTheChangeAsTheCommit(t *testing.T) {
	repo := newTestRepo(t)
	when := time.Date(2019, 5, 4, 3, 2, 1, 0, time.FixedZone("plus-two", 2*3600))
	change := core.Change{
		// core says a message may span lines, so one does.
		Message: "Add the new post\n\nIt explains the thing, at length,\nover several lines.\n",
		Author:  core.Author{Name: "Wilkins Micawber", Email: "wilkins@example.invalid"},
		Time:    when,
	}

	if _, err := repo.open().Put(t.Context(), newContent(t, "posts/new.md", "new", "\nhello\n"), change); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if got := repo.commitField("%an"); got != change.Author.Name {
		t.Errorf("commit author name = %q, want %q", got, change.Author.Name)
	}
	if got := repo.commitField("%ae"); got != change.Author.Email {
		t.Errorf("commit author e-mail = %q, want %q", got, change.Author.Email)
	}
	if got := repo.commitField("%B"); strings.TrimRight(got, "\n") != strings.TrimRight(change.Message, "\n") {
		t.Errorf("commit message = %q, want %q", got, change.Message)
	}
	got, err := time.Parse(time.RFC3339, repo.commitField("%aI"))
	if err != nil {
		t.Fatalf("parse the commit's author time: %v", err)
	}
	if !got.Equal(when) {
		t.Errorf("commit author time = %v, want %v", got, when)
	}
}

// TestPutWithAZeroChangeTimeUsesTheBackendClock is the rule core states: a zero
// Change.Time means the backend supplies its own clock.
func TestPutWithAZeroChangeTimeUsesTheBackendClock(t *testing.T) {
	repo := newTestRepo(t)
	change := core.Change{Message: "no clock", Author: core.Author{Name: "A", Email: "a@example.invalid"}}
	before := time.Now().Add(-2 * time.Minute)

	if _, err := repo.open().Put(t.Context(), newContent(t, "a.md", "a", "\na\n"), change); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := time.Parse(time.RFC3339, repo.commitField("%aI"))
	if err != nil {
		t.Fatalf("parse the commit's author time: %v", err)
	}
	if got.Before(before) || got.After(time.Now().Add(2*time.Minute)) {
		t.Errorf("commit author time = %v, want roughly now", got)
	}
}

// TestPutRejectsInvalidContentAndInvalidChange leaves the rules where core wrote
// them: the adapter adds context and does not restate them.
func TestPutRejectsInvalidContentAndInvalidChange(t *testing.T) {
	repo := newTestRepo(t)
	opened := repo.open()
	valid := newContent(t, "a.md", "a", "\na\n")

	cases := []struct {
		name    string
		content core.Content
		change  core.Change
	}{
		{"no path", core.Content{FrontMatter: valid.FrontMatter, Body: valid.Body}, testChange("m")},
		{"bad front matter", core.Content{Path: valid.Path, FrontMatter: core.FrontMatter{Format: "xml"}}, testChange("m")},
		{"empty message", valid, core.Change{Author: core.Author{Name: "A"}}},
		{"blank message", valid, core.Change{Message: "   ", Author: core.Author{Name: "A"}}},
		{"no author", valid, core.Change{Message: "m"}},
		{"author e-mail with no at sign", valid, core.Change{Message: "m", Author: core.Author{Name: "A", Email: "nope"}}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := opened.Put(t.Context(), c.content, c.change); !errors.Is(err, core.ErrInvalid) {
				t.Errorf("Put: got %v, want an error matching core.ErrInvalid", err)
			}
			if _, born := repo.tip(); born {
				t.Errorf("a refused write made a commit")
			}
		})
	}
}

// TestPutOnAnUnbornBranchWritesTheFirstCommit is how a repository somebody has
// only run git init in gets its content.
func TestPutOnAnUnbornBranchWritesTheFirstCommit(t *testing.T) {
	repo := newTestRepo(t)

	rev, err := repo.open().Put(t.Context(), newContent(t, "posts/first.md", "first", "\nfirst\n"), testChange("the first commit"))
	if err != nil {
		t.Fatalf("Put on an unborn branch: %v", err)
	}
	if got := repo.commitCount(); got != 1 {
		t.Errorf("commit count = %d, want 1", got)
	}
	if got := repo.commitField("%P"); got != "" {
		t.Errorf("the first commit has parent %q, want none", got)
	}
	if want := core.Revision(repo.blobAt("posts/first.md")); rev != want {
		t.Errorf("Put returned %q, want %q", rev, want)
	}
}

// TestPutDoesNotTouchTheWorkingTreeOrTheUsersIndex is the test that fails the
// moment somebody reaches for "git add".
//
// The adapter works at the object layer precisely so that a human with the same
// checkout open is not fought over; writing through a working tree would run
// their hooks, apply their filters and stage their uncommitted work.
func TestPutDoesNotTouchTheWorkingTreeOrTheUsersIndex(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"posts/a.md": []byte("---\ntitle: a\n---\n\na\n")})
	repo.checkout()

	indexPath := filepath.Join(repo.dir, ".git", "index")
	before, err := os.Stat(indexPath)
	if err != nil {
		t.Fatalf("stat the user's index: %v", err)
	}
	worktreePath := filepath.Join(repo.dir, "posts", "a.md")
	worktreeBefore, err := os.ReadFile(worktreePath)
	if err != nil {
		t.Fatalf("read the working tree: %v", err)
	}

	if _, err := repo.open().Put(t.Context(), newContent(t, "posts/b.md", "b", "\nb\n"), testChange("add b")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	after, err := os.Stat(indexPath)
	if err != nil {
		t.Fatalf("stat the user's index after the write: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Errorf("the user's index changed: %v/%d became %v/%d", before.ModTime(), before.Size(), after.ModTime(), after.Size())
	}
	if worktreeAfter, err := os.ReadFile(worktreePath); err != nil || !bytes.Equal(worktreeAfter, worktreeBefore) {
		t.Errorf("the working tree changed: %q became %q (%v)", worktreeBefore, worktreeAfter, err)
	}
	if _, err := os.Stat(filepath.Join(repo.dir, "posts", "b.md")); !os.IsNotExist(err) {
		t.Errorf("the write appeared in the working tree; the adapter is not staying at the object layer")
	}
}

// TestPutCommitsAreDeterministic is what lets a later test assert an exact object
// id when that is the clearest assertion, and is also the property that makes a
// fixture reproducible between machines.
func TestPutCommitsAreDeterministic(t *testing.T) {
	content := newContent(t, "posts/a.md", "a", "\nhello\n")
	change := testChange("the same change")

	oids := make([]string, 2)
	for i := range oids {
		repo := newTestRepo(t)
		if _, err := repo.open().Put(t.Context(), content, change); err != nil {
			t.Fatalf("Put: %v", err)
		}
		oids[i] = repo.head()
	}
	if oids[0] != oids[1] {
		t.Errorf("the same write into two fresh repositories produced %q and %q", oids[0], oids[1])
	}
}

// TestPutCreatesIntermediateDirectories checks that nothing has to walk the path:
// update-index creates every tree the path needs.
func TestPutCreatesIntermediateDirectories(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"a.md": []byte("---\ntitle: a\n---\n\na\n")})

	if _, err := repo.open().Put(t.Context(), newContent(t, "deep/nested/new/post.md", "deep", "\ndeep\n"), testChange("go deep")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got := repo.blobAt("deep/nested/new/post.md"); got == "" {
		t.Error("the document is not in the tree")
	}
}

// TestPutHonoursContentRoot writes through the root without the caller ever
// naming it.
func TestPutHonoursContentRoot(t *testing.T) {
	repo := newTestRepo(t)
	opened := repo.open(WithContentRoot(mustCollection(t, "content")))

	rev, err := opened.Put(t.Context(), newContent(t, "posts/a.md", "a", "\na\n"), testChange("add a"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if want := core.Revision(repo.blobAt("content/posts/a.md")); rev != want {
		t.Errorf("Put returned %q, want the blob at content/posts/a.md, %q", rev, want)
	}
}

// TestPutHonoursContextCancellationAndPublishesNothing is the cancellation
// guarantee stated as behaviour.
//
// The interleaving is injected rather than raced: the context is cancelled just
// before the ref would be published, which is the last step and the only visible
// one. What must be true afterwards is that the branch has not moved and that
// what the write did produce is unreferenced objects, which "git gc" reclaims.
func TestPutHonoursContextCancellationAndPublishesNothing(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"posts/a.md": []byte("---\ntitle: a\n---\n\na\n")})
	before := repo.head()

	ctx, cancel := context.WithCancel(t.Context())
	opened := repo.open()
	opened.exec.observe = func(args []string) {
		if args[0] == "update-ref" {
			cancel()
		}
	}

	_, err := opened.Put(ctx, newContent(t, "posts/b.md", "b", "\nb\n"), testChange("add b"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Put cancelled before publishing: got %v, want an error matching context.Canceled", err)
	}
	if repo.head() != before {
		t.Fatal("the branch moved; a cancelled write published something")
	}

	// The blob it did write is still in the object database, referenced by
	// nothing. That is ordinary Git and is what git gc is for.
	blob := strings.TrimSpace(repo.git(nil, []byte("---\ntitle: b\n---\n\nb\n"), "hash-object", "--stdin"))
	if _, err := repo.tryGit(nil, nil, "cat-file", "-e", blob); err != nil {
		t.Errorf("the blob the cancelled write hashed is not in the object database: %v", err)
	}
	repo.fsck()
}

// TestPutUpdateReturnsANewRevision is the update half of the compare-and-swap
// working normally: read, edit, write, and the revision moves with the bytes.
func TestPutUpdateReturnsANewRevision(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"posts/a.md": []byte("---\ntitle: a\n---\n\noriginal\n")})
	opened := repo.open()

	got, err := opened.Get(t.Context(), mustPath(t, "posts/a.md"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got.Body = []byte("\nedited\n")

	rev, err := opened.Put(t.Context(), got, testChange("edit a"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if rev == got.Revision {
		t.Errorf("Put returned the revision it replaced, %q, though the bytes changed", rev)
	}
	if want := core.Revision(repo.blobAt("posts/a.md")); rev != want {
		t.Errorf("Put returned %q, want %q", rev, want)
	}
	if want := []byte("---\ntitle: a\n---\n\nedited\n"); !bytes.Equal(repo.blob("posts/a.md"), want) {
		t.Errorf("stored bytes = %q, want %q", repo.blob("posts/a.md"), want)
	}
	if got := repo.commitCount(); got != 2 {
		t.Errorf("commit count = %d, want 2", got)
	}
}

// TestPutUpdateWithAStaleRevisionIsErrConflict injects the interleaving rather
// than racing for it: the document is committed over by plain git between the
// read and the write, which is exactly what a second editor would do.
func TestPutUpdateWithAStaleRevisionIsErrConflict(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"posts/a.md": []byte("---\ntitle: a\n---\n\noriginal\n")})
	opened := repo.open()

	stale, err := opened.Get(t.Context(), mustPath(t, "posts/a.md"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	repo.commit("somebody else", map[string][]byte{"posts/a.md": []byte("---\ntitle: a\n---\n\ntheirs\n")})
	before := repo.head()

	stale.Body = []byte("\nmine\n")
	if _, err := opened.Put(t.Context(), stale, testChange("edit a")); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("Put with a stale revision: got %v, want an error matching core.ErrConflict", err)
	}
	if repo.head() != before {
		t.Error("the branch moved for a refused write")
	}
	if want := []byte("---\ntitle: a\n---\n\ntheirs\n"); !bytes.Equal(repo.blob("posts/a.md"), want) {
		t.Errorf("stored bytes = %q, want the other writer's %q", repo.blob("posts/a.md"), want)
	}
}

// TestPutUpdateOfADeletedDocumentIsErrNotFound distinguishes "somebody changed
// this" from "somebody removed this", which are different things for a caller to
// show a user.
func TestPutUpdateOfADeletedDocumentIsErrNotFound(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{
		"posts/a.md": []byte("---\ntitle: a\n---\n\na\n"),
		"posts/b.md": []byte("---\ntitle: b\n---\n\nb\n"),
	})
	opened := repo.open()

	stale, err := opened.Get(t.Context(), mustPath(t, "posts/a.md"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	repo.remove("somebody else removed it", "posts/a.md")

	stale.Body = []byte("\nmine\n")
	if _, err := opened.Put(t.Context(), stale, testChange("edit a")); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Put over a deleted document: got %v, want an error matching core.ErrNotFound", err)
	}
}

// TestPutPreservesTheStoredLayout is the phase-2 open question answered in one
// case, with the table below answering it in all seven.
func TestPutPreservesTheStoredLayout(t *testing.T) {
	repo := newTestRepo(t)
	stored := []byte("---\r\ntitle: a\r\n---\r\n\r\noriginal\r\n")
	repo.commit("first", map[string][]byte{"posts/a.md": stored})
	opened := repo.open()

	got, err := opened.Get(t.Context(), mustPath(t, "posts/a.md"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got.Body = []byte("\r\nedited\r\n")

	if _, err := opened.Put(t.Context(), got, testChange("edit a")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if want := []byte("---\r\ntitle: a\r\n---\r\n\r\nedited\r\n"); !bytes.Equal(repo.blob("posts/a.md"), want) {
		t.Errorf("stored bytes = %q, want %q; the CRLF delimiters did not survive", repo.blob("posts/a.md"), want)
	}
}

// TestPutPreservesTheStoredLayoutForEveryFormat is the test the whole phase-2
// guarantee rests on.
//
// The caller sees only a core.Content: no markdown.Layout crosses
// core.ContentRepository in either direction, which is the point. If this test
// could be made to pass only by adding one to core.Content, the design would be
// wrong. Put recovers the shape from the stored object it must read anyway for
// the revision check, so the read costs nothing extra and no cache is needed.
func TestPutPreservesTheStoredLayoutForEveryFormat(t *testing.T) {
	cases := []struct {
		name   string
		stored []byte
		body   []byte
		edited []byte
	}{
		{"crlf", []byte("---\r\ntitle: a\r\n---\r\n\r\nold\r\n"), []byte("\r\nold\r\n"), []byte("\r\nnew\r\n")},
		{"byte order mark", []byte("\ufeff---\ntitle: a\n---\n\nold\n"), []byte("\nold\n"), []byte("\nnew\n")},
		{"delimiter whitespace", []byte("---  \ntitle: a\n---\t\n\nold\n"), []byte("\nold\n"), []byte("\nnew\n")},
		{"toml", []byte("+++\ntitle = 'a'\n+++\n\nold\n"), []byte("\nold\n"), []byte("\nnew\n")},
		{"json", []byte("{\"title\": \"a\"}\n\nold\n"), []byte("\nold\n"), []byte("\nnew\n")},
		{"no front matter", []byte("just prose\n\nold\n"), []byte("just prose\n\nold\n"), []byte("just prose\n\nnew\n")},
		{"closing delimiter at end of file", []byte("---\ntitle: a\n---"), []byte(""), []byte("\nnew\n")},
	}

	repo := newTestRepo(t)
	files := map[string][]byte{}
	for _, c := range cases {
		files[c.name+".md"] = c.stored
	}
	repo.commit("first", files)
	opened := repo.open()

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := mustPath(t, c.name+".md")

			got, err := opened.Get(t.Context(), path)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if !bytes.Equal(got.Body, c.body) {
				t.Fatalf("Body = %q, want %q", got.Body, c.body)
			}

			// Only the body is edited. The caller has no way to say anything
			// about the delimiters, and does not need one.
			got.Body = c.edited
			if _, err := opened.Put(t.Context(), got, testChange("edit "+c.name)); err != nil {
				t.Fatalf("Put: %v", err)
			}

			prefix := c.stored[:len(c.stored)-len(c.body)]
			want := append(append([]byte{}, prefix...), c.edited...)
			if stored := repo.blob(c.name + ".md"); !bytes.Equal(stored, want) {
				t.Errorf("stored bytes = %q, want %q; everything before the body must be untouched", stored, want)
			}
		})
	}
}

// TestPutOfByteIdenticalContentIsANoOp is the behaviour a content-addressed
// revision forces, written down.
//
// An editor that saves without changing anything, or an edit reverted before
// saving, produces byte-identical output -- precisely because the stored layout
// is preserved. Returning ErrConflict would be untrue, since nothing conflicted;
// writing an empty commit would put a change that changed nothing into the
// user's history. Doing nothing quietly is the only honest answer.
func TestPutOfByteIdenticalContentIsANoOp(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"posts/a.md": []byte("---\r\ntitle: a\r\n---\r\n\r\nbody\r\n")})
	opened := repo.open()
	before := repo.head()
	commitsBefore := repo.commitCount()

	got, err := opened.Get(t.Context(), mustPath(t, "posts/a.md"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	rev, err := opened.Put(t.Context(), got, testChange("save without changing anything"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if rev != got.Revision {
		t.Errorf("Put returned %q, want the revision the caller already held, %q", rev, got.Revision)
	}
	if repo.head() != before {
		t.Error("the branch moved for a write that changed nothing")
	}
	if after := repo.commitCount(); after != commitsBefore {
		t.Errorf("commit count went from %d to %d; an empty commit was made", commitsBefore, after)
	}
}

// TestPutOfADamagedDocumentFallsBackToTheCanonicalLayout is the one place the
// layout guarantee does not apply, and it applies to a file that had no coherent
// shape to preserve.
//
// The document is reachable because List does not decode, so a caller can find
// it, take its revision, and write over it. That is how a bad merge gets fixed.
func TestPutOfADamagedDocumentFallsBackToTheCanonicalLayout(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"posts/broken.md": []byte("---\ntitle: a\nnever terminated\n")})
	opened := repo.open()

	entries, err := opened.List(t.Context(), core.Collection{})
	if err != nil || len(entries) != 1 {
		t.Fatalf("List = %v, %v; want the damaged document listed", entries, err)
	}

	fixed := newContent(t, "posts/broken.md", "fixed", "\nrepaired\n")
	fixed.Revision = entries[0].Revision
	if _, err := opened.Put(t.Context(), fixed, testChange("repair the document")); err != nil {
		t.Fatalf("Put over a damaged document: %v", err)
	}
	if want := []byte("---\ntitle: fixed\n---\n\nrepaired\n"); !bytes.Equal(repo.blob("posts/broken.md"), want) {
		t.Errorf("stored bytes = %q, want the canonical shape %q", repo.blob("posts/broken.md"), want)
	}
	if _, err := opened.Get(t.Context(), mustPath(t, "posts/broken.md")); err != nil {
		t.Errorf("Get after the repair: %v", err)
	}
}

// TestPutSucceedsWhenAnUnrelatedDocumentMovedTheRef is the test that
// discriminates between the two possible answers to "what is a Revision?".
//
// Alice reads posts/a.md. Bob commits posts/b.md. Alice writes. It must succeed:
// nothing she touched changed. Any implementation whose Revision is a commit id
// fails this, which is why it is written this way round -- it is the reason the
// blob object id was chosen, made executable.
func TestPutSucceedsWhenAnUnrelatedDocumentMovedTheRef(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"posts/a.md": []byte("---\ntitle: a\n---\n\na\n")})
	opened := repo.open()

	alice, err := opened.Get(t.Context(), mustPath(t, "posts/a.md"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	repo.commit("bob writes something else", map[string][]byte{"posts/b.md": []byte("---\ntitle: b\n---\n\nb\n")})

	alice.Body = []byte("\nalice edited this\n")
	if _, err := opened.Put(t.Context(), alice, testChange("alice edits a")); err != nil {
		t.Fatalf("Put after an unrelated document moved the branch: %v; a commit-id revision would fail exactly here", err)
	}
	if want := []byte("---\ntitle: a\n---\n\nalice edited this\n"); !bytes.Equal(repo.blob("posts/a.md"), want) {
		t.Errorf("stored bytes = %q, want %q", repo.blob("posts/a.md"), want)
	}
	// Bob's write is still there: the retry rebuilt on his commit rather than
	// over it.
	if repo.blobAt("posts/b.md") == "" {
		t.Error("the other writer's document is gone; the retry rebuilt from a stale parent")
	}
}

// TestPutRetriesAreBounded drives the loop past its limit and requires a wrapped
// error rather than a hang.
//
// The branch is moved by plain git before every publish, and the document under
// edit is left alone, so the retry can never find a conflict to report and can
// never win the compare-and-swap. The context carries a deadline so that a
// regression in the bound fails the test instead of blocking the suite.
func TestPutRetriesAreBounded(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"posts/a.md": []byte("---\ntitle: a\n---\n\na\n")})
	opened := repo.open()

	got, err := opened.Get(t.Context(), mustPath(t, "posts/a.md"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	attempts := 0
	opened.exec.observe = func(args []string) {
		if args[0] != "update-ref" {
			return
		}
		attempts++
		repo.commit(fmt.Sprintf("somebody else, again (%d)", attempts), map[string][]byte{
			mustPath(t, fmt.Sprintf("posts/other-%d.md", attempts)).String(): []byte("---\ntitle: o\n---\n\no\n"),
		})
	}

	got.Body = []byte("\nedited\n")
	_, err = opened.Put(ctx, got, testChange("edit a"))
	if err == nil {
		t.Fatal("Put: got nil error, want the bound to have been reported")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Put ran until the test's deadline: the retry is not bounded")
	}
	if attempts != maxPublishAttempts {
		t.Errorf("published %d times, want %d attempts", attempts, maxPublishAttempts)
	}
	if !strings.Contains(err.Error(), fmt.Sprint(maxPublishAttempts)) {
		t.Errorf("Error() = %q, want the number of attempts named in it", err.Error())
	}
	for _, sentinel := range []error{core.ErrConflict, core.ErrNotFound, core.ErrExists} {
		if errors.Is(err, sentinel) {
			t.Errorf("Error() matches %v; exhausting the retry is not one of core's conditions", sentinel)
		}
	}
}

// TestPutDoesNotRetainOrMutateTheContent is core's obligation on every
// implementation: the caller owns what it passed and may reuse it afterwards.
//
// This adapter satisfies it almost by construction, because it serialises to
// bytes on the way in and keeps nothing -- which is exactly why it is worth a
// test rather than an assumption.
func TestPutDoesNotRetainOrMutateTheContent(t *testing.T) {
	repo := newTestRepo(t)
	opened := repo.open()

	content := newContent(t, "posts/a.md", "a", "\nbody\n")
	body := bytes.Clone(content.Body)
	fields := map[string]any{"title": "a"}

	rev, err := opened.Put(t.Context(), content, testChange("create a"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !bytes.Equal(content.Body, body) {
		t.Errorf("Put changed the caller's Body: %q became %q", body, content.Body)
	}
	if len(content.FrontMatter.Fields) != len(fields) || content.FrontMatter.Fields["title"] != fields["title"] {
		t.Errorf("Put changed the caller's front matter: %v, want %v", content.FrontMatter.Fields, fields)
	}

	// The same again through the update path, which is the one that reads the
	// stored object and could be tempted to reuse the caller's slices.
	content.Revision = rev
	content.Body = []byte("\nedited\n")
	body = bytes.Clone(content.Body)
	if _, err := opened.Put(t.Context(), content, testChange("edit a")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !bytes.Equal(content.Body, body) {
		t.Errorf("Put changed the caller's Body: %q became %q", body, content.Body)
	}

	// Nothing the adapter kept can be reached by mutating what the caller still
	// holds, so a later read is unaffected by it.
	for i := range content.Body {
		content.Body[i] = 'X'
	}
	got, err := opened.Get(t.Context(), content.Path)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if want := []byte("\nedited\n"); !bytes.Equal(got.Body, want) {
		t.Errorf("Body = %q, want %q; the adapter retained the caller's slice", got.Body, want)
	}
}
