package git

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shutx-net/micawber/core"
)

// historyFixture builds three commits to one document, with a distinct author
// and message each, plus a second document so that path filtering has something
// to filter out.
func historyFixture(t *testing.T) *testRepo {
	t.Helper()

	repo := newTestRepo(t)
	repo.commitFixture(fixtureCommit{
		message: "Add the post",
		name:    "Wilkins Micawber",
		email:   "wilkins@example.invalid",
		date:    "2001-02-03T04:05:06+00:00",
		entries: regularFiles(map[string][]byte{"posts/a.md": []byte("---\ntitle: a\n---\n\nfirst\n")}),
	})
	repo.commitFixture(fixtureCommit{
		message: "Something else entirely",
		entries: regularFiles(map[string][]byte{"posts/b.md": []byte("---\ntitle: b\n---\n\nb\n")}),
	})
	repo.commitFixture(fixtureCommit{
		message: "Revise the post\n\nWith a second paragraph.\n",
		name:    "Emma Micawber",
		email:   "emma@example.invalid",
		date:    "2004-05-06T07:08:09+00:00",
		entries: regularFiles(map[string][]byte{"posts/a.md": []byte("---\ntitle: a\n---\n\nsecond\n")}),
	})
	repo.commitFixture(fixtureCommit{
		message: "Revise it again",
		name:    "Wilkins Micawber",
		email:   "wilkins@example.invalid",
		date:    "2007-08-09T10:11:12+00:00",
		entries: regularFiles(map[string][]byte{"posts/a.md": []byte("---\ntitle: a\n---\n\nthird\n")}),
	})
	return repo
}

// TestHistoryReturnsRevisionsMostRecentFirst is the ordering core specifies, and
// the path filtering that makes the list about one document.
func TestHistoryReturnsRevisionsMostRecentFirst(t *testing.T) {
	repo := historyFixture(t)

	infos, err := repo.open().History(t.Context(), mustPath(t, "posts/a.md"), 0)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(infos) != 3 {
		t.Fatalf("History returned %d entries, want 3 (the commit touching the other document must not appear)", len(infos))
	}
	if got := infos[0].Message; !strings.HasPrefix(got, "Revise it again") {
		t.Errorf("the first entry is %q, want the most recent change", got)
	}
	for i := 1; i < len(infos); i++ {
		if !infos[i].Time.Before(infos[i-1].Time) {
			t.Errorf("entry %d is dated %v, not before entry %d at %v", i, infos[i].Time, i-1, infos[i-1].Time)
		}
	}
	if want := core.Revision(repo.blobAt("posts/a.md")); infos[0].Revision != want {
		t.Errorf("the most recent revision is %q, want what the document is now, %q", infos[0].Revision, want)
	}
}

// TestHistoryReportsAuthorTimeAndMessage checks each field against what git
// recorded, including a message that spans lines.
func TestHistoryReportsAuthorTimeAndMessage(t *testing.T) {
	repo := historyFixture(t)

	infos, err := repo.open().History(t.Context(), mustPath(t, "posts/a.md"), 0)
	if err != nil {
		t.Fatalf("History: %v", err)
	}

	middle := infos[1]
	if middle.Author.Name != "Emma Micawber" || middle.Author.Email != "emma@example.invalid" {
		t.Errorf("author = %v, want Emma Micawber <emma@example.invalid>", middle.Author)
	}
	if want := "Revise the post\n\nWith a second paragraph.\n"; strings.TrimRight(middle.Message, "\n") != strings.TrimRight(want, "\n") {
		t.Errorf("message = %q, want %q", middle.Message, want)
	}
	when, err := time.Parse(time.RFC3339, "2004-05-06T07:08:09+00:00")
	if err != nil {
		t.Fatalf("parse the expected time: %v", err)
	}
	if !middle.Time.Equal(when) {
		t.Errorf("time = %v, want %v", middle.Time, when)
	}
	// The author, not the committer: core.Author is who the change is attributed
	// to, and the two can differ.
	if got := repo.commitField("%cn"); got == "" {
		t.Fatal("the fixture has no committer, so this distinction is untested")
	}
}

// TestHistoryLimitCapsTheResult is the limit doing what core says it does.
func TestHistoryLimitCapsTheResult(t *testing.T) {
	repo := historyFixture(t)
	opened := repo.open()

	infos, err := opened.History(t.Context(), mustPath(t, "posts/a.md"), 2)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("History with limit 2 returned %d entries", len(infos))
	}
	if !strings.HasPrefix(infos[0].Message, "Revise it again") {
		t.Errorf("a limit dropped the wrong end: the first entry is %q", infos[0].Message)
	}

	if infos, err := opened.History(t.Context(), mustPath(t, "posts/a.md"), 99); err != nil || len(infos) != 3 {
		t.Errorf("History with a limit above the length returned %d entries (%v), want 3", len(infos), err)
	}
}

// TestHistoryWithAZeroOrNegativeLimitIsUnlimited is core's rule stated exactly:
// zero or less means no limit.
func TestHistoryWithAZeroOrNegativeLimitIsUnlimited(t *testing.T) {
	repo := historyFixture(t)
	opened := repo.open()

	for _, limit := range []int{0, -1, -100} {
		infos, err := opened.History(t.Context(), mustPath(t, "posts/a.md"), limit)
		if err != nil {
			t.Fatalf("History with limit %d: %v", limit, err)
		}
		if len(infos) != 3 {
			t.Errorf("History with limit %d returned %d entries, want all 3", limit, len(infos))
		}
	}
}

// TestHistoryOfAMissingPathIsErrNotFound covers a path nothing was ever
// committed to, which git reports as an empty log rather than as a failure.
func TestHistoryOfAMissingPathIsErrNotFound(t *testing.T) {
	repo := historyFixture(t)

	_, err := repo.open().History(t.Context(), mustPath(t, "posts/nope.md"), 0)
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("History of a path with no history: got %v, want an error matching core.ErrNotFound", err)
	}
}

// TestHistoryOfADeletedDocumentStillReturnsIt records a decision worth being
// explicit about: history survives the document.
//
// A document that has been deleted has no current revision but does have past
// ones, and those are exactly what a caller needs in order to restore it.
// Refusing here would make a deletion unrecoverable through this interface.
func TestHistoryOfADeletedDocumentStillReturnsIt(t *testing.T) {
	repo := historyFixture(t)
	repo.remove("Retire the post", "posts/a.md")

	infos, err := repo.open().History(t.Context(), mustPath(t, "posts/a.md"), 0)
	if err != nil {
		t.Fatalf("History of a deleted document: %v", err)
	}
	if len(infos) != 3 {
		t.Errorf("History returned %d entries, want the 3 revisions it had before it was deleted", len(infos))
	}
}

// TestHistoryOnAnUnbornBranchIsErrNotFound is the same absence where there is no
// commit at all.
func TestHistoryOnAnUnbornBranchIsErrNotFound(t *testing.T) {
	repo := newTestRepo(t)

	_, err := repo.open().History(t.Context(), mustPath(t, "posts/a.md"), 0)
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("History on an unborn branch: got %v, want an error matching core.ErrNotFound", err)
	}
}

// TestHistoryRevisionsAreBlobIdsUsableWithGetRevision ties the two methods
// together: everything History returns, GetRevision accepts.
func TestHistoryRevisionsAreBlobIdsUsableWithGetRevision(t *testing.T) {
	repo := historyFixture(t)
	opened := repo.open()
	p := mustPath(t, "posts/a.md")

	infos, err := opened.History(t.Context(), p, 0)
	if err != nil {
		t.Fatalf("History: %v", err)
	}

	wantBodies := []string{"\nthird\n", "\nsecond\n", "\nfirst\n"}
	for i, info := range infos {
		got, err := opened.GetRevision(t.Context(), p, info.Revision)
		if err != nil {
			t.Errorf("GetRevision(%q): %v", info.Revision, err)
			continue
		}
		if string(got.Body) != wantBodies[i] {
			t.Errorf("GetRevision(%q).Body = %q, want %q", info.Revision, got.Body, wantBodies[i])
		}
		if got.Revision != info.Revision {
			t.Errorf("GetRevision returned revision %q, want %q", got.Revision, info.Revision)
		}
		if got.Path != p {
			t.Errorf("GetRevision returned path %q, want %q", got.Path, p)
		}
	}

	current, err := opened.Get(t.Context(), p)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if current.Revision != infos[0].Revision {
		t.Errorf("Get says %q, History's most recent says %q", current.Revision, infos[0].Revision)
	}
}

// TestHistoryOfARevertedDocumentRepeatsTheRevision records the consequence of a
// content-addressed revision honestly rather than hiding it.
//
// A blob id repeats when the bytes repeat, so a document reverted to earlier
// content reports the earlier revision again, and a revision therefore does not
// identify a unique commit.
func TestHistoryOfARevertedDocumentRepeatsTheRevision(t *testing.T) {
	repo := newTestRepo(t)
	a := []byte("---\ntitle: a\n---\n\nA\n")
	b := []byte("---\ntitle: a\n---\n\nB\n")
	repo.commit("A", map[string][]byte{"posts/a.md": a})
	repo.commit("B", map[string][]byte{"posts/a.md": b})
	repo.commit("A again", map[string][]byte{"posts/a.md": a})

	infos, err := repo.open().History(t.Context(), mustPath(t, "posts/a.md"), 0)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(infos) != 3 {
		t.Fatalf("History returned %d entries, want 3", len(infos))
	}
	if infos[0].Revision != infos[2].Revision {
		t.Errorf("the reverted revision is %q and the original %q; identical bytes must have identical revisions", infos[0].Revision, infos[2].Revision)
	}
	if infos[0].Revision == infos[1].Revision {
		t.Errorf("two different documents share the revision %q", infos[0].Revision)
	}
	// The revision repeats, so it names two commits. That is the price of
	// per-document compare-and-swap, and it is why a revision is not a commit.
	if infos[0].Message == infos[2].Message {
		t.Errorf("the two entries with the same revision have the same message %q; they should be distinct commits", infos[0].Message)
	}
}

// TestGetRevisionReturnsTheContentAsItStoodAtThatRevision is the method doing
// its job, byte for byte, with the layout of the day.
func TestGetRevisionReturnsTheContentAsItStoodAtThatRevision(t *testing.T) {
	repo := newTestRepo(t)
	old := []byte("---\r\ntitle: a\r\n---\r\n\r\nold\r\n")
	repo.commit("first", map[string][]byte{"posts/a.md": old})
	oldRev := core.Revision(repo.blobAt("posts/a.md"))
	repo.commit("second", map[string][]byte{"posts/a.md": []byte("---\ntitle: a\n---\n\nnew\n")})

	got, err := repo.open().GetRevision(t.Context(), mustPath(t, "posts/a.md"), oldRev)
	if err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	if want := []byte("\r\nold\r\n"); !bytes.Equal(got.Body, want) {
		t.Errorf("Body = %q, want %q", got.Body, want)
	}
	if !bytes.Equal(got.FrontMatter.Raw, []byte("title: a\r\n")) {
		t.Errorf("FrontMatter.Raw = %q, want the bytes of the day", got.FrontMatter.Raw)
	}
}

// TestGetRevisionRejectsARevisionThatIsNotInThisPathsHistory is the security
// test, written to look like the attack it prevents.
//
// "git cat-file blob" will return any object in the database. Without a
// membership check, GetRevision(p, rev) is an arbitrary-object read oracle
// wearing a path argument, and every path validation core performs is bypassed by
// anyone who can supply an object id. The three cases below are the same hole
// through three different doors.
func TestGetRevisionRejectsARevisionThatIsNotInThisPathsHistory(t *testing.T) {
	repo := newTestRepo(t)
	secret := "ghp_111111111111111111111111111111111111"
	repo.commit("first", map[string][]byte{
		"content/posts/public.md": []byte("---\ntitle: public\n---\n\npublic\n"),
		"secret/private.md":       []byte("---\ntoken: " + secret + "\n---\n\nprivate\n"),
	})
	repo.git(nil, nil, "branch", "other")
	repo.ref = "refs/heads/other"
	repo.commit("on another branch", map[string][]byte{"content/posts/elsewhere.md": []byte("---\ntitle: elsewhere\n---\n\nelsewhere\n")})
	onOtherBranch := core.Revision(repo.blobAt("content/posts/elsewhere.md"))
	repo.ref = "refs/heads/main"

	opened := repo.open(WithContentRoot(mustCollection(t, "content")))
	target := mustPath(t, "posts/public.md")

	cases := []struct {
		name string
		rev  core.Revision
	}{
		{"a document outside the content root", core.Revision(repo.blobAt("secret/private.md"))},
		{"a document on another branch", onOtherBranch},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// The object really is in the database, so this is a refusal rather
			// than an accident of the object not existing.
			if _, err := repo.tryGit(nil, nil, "cat-file", "-e", string(c.rev)); err != nil {
				t.Fatalf("the fixture object %q is not in the database: %v", c.rev, err)
			}

			got, err := opened.GetRevision(t.Context(), target, c.rev)
			if !errors.Is(err, core.ErrNotFound) {
				t.Fatalf("GetRevision with %s: got %v, want an error matching core.ErrNotFound", c.name, err)
			}
			if len(got.Body) != 0 || got.FrontMatter.Fields != nil {
				t.Errorf("GetRevision returned content: %q / %v", got.Body, got.FrontMatter.Fields)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "private") {
				t.Errorf("Error() = %q; it leaks the document it refused to return", err.Error())
			}
		})
	}
}

// TestGetRevisionOfAnUnknownRevisionIsErrNotFound covers an object id that names
// nothing at all, and one that is not an object id.
func TestGetRevisionOfAnUnknownRevisionIsErrNotFound(t *testing.T) {
	repo := historyFixture(t)
	opened := repo.open()
	p := mustPath(t, "posts/a.md")

	for _, rev := range []core.Revision{
		"0000000000000000000000000000000000000000",
		"not an object id",
		"",
		"HEAD",
		"--upload-pack=x",
	} {
		if _, err := opened.GetRevision(t.Context(), p, rev); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("GetRevision(%q): got %v, want an error matching core.ErrNotFound", rev, err)
		}
	}
}

// TestGetRevisionOfADamagedPastVersionIsErrInvalid keeps the same rule Get
// applies to the present: a file that will not parse has no honest Content.
func TestGetRevisionOfADamagedPastVersionIsErrInvalid(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("a bad merge", map[string][]byte{"posts/a.md": []byte("---\ntitle: a\nnever terminated\n")})
	damaged := core.Revision(repo.blobAt("posts/a.md"))
	repo.commit("repaired", map[string][]byte{"posts/a.md": []byte("---\ntitle: a\n---\n\nfixed\n")})

	_, err := repo.open().GetRevision(t.Context(), mustPath(t, "posts/a.md"), damaged)
	if !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("GetRevision of a damaged past version: got %v, want an error matching core.ErrInvalid", err)
	}
}

// TestHistoryHonoursContentRootAndContextCancellation covers the two obligations
// every operation shares.
func TestHistoryHonoursContentRootAndContextCancellation(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{
		"content/posts/a.md": []byte("---\ntitle: a\n---\n\na\n"),
		"secret/b.md":        []byte("---\ntitle: b\n---\n\nb\n"),
	})
	opened := repo.open(WithContentRoot(mustCollection(t, "content")))

	infos, err := opened.History(t.Context(), mustPath(t, "posts/a.md"), 0)
	if err != nil {
		t.Fatalf("History within the content root: %v", err)
	}
	if len(infos) != 1 {
		t.Errorf("History returned %d entries, want 1", len(infos))
	}
	if _, err := opened.History(t.Context(), mustPath(t, "secret/b.md"), 0); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("History of a path outside the content root: got %v, want an error matching core.ErrNotFound", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := opened.History(ctx, mustPath(t, "posts/a.md"), 0); !errors.Is(err, context.Canceled) {
		t.Errorf("History with a cancelled context: got %v, want an error matching context.Canceled", err)
	}
	if _, err := opened.GetRevision(ctx, mustPath(t, "posts/a.md"), infos[0].Revision); !errors.Is(err, context.Canceled) {
		t.Errorf("GetRevision with a cancelled context: got %v, want an error matching context.Canceled", err)
	}
}
