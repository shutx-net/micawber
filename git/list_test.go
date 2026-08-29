package git

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/shutx-net/micawber/core"
)

// listFixture is the one repository the listing tests share: it covers
// recursion, extension filtering, sorting and collection bounding in a single
// commit, so each test states one rule rather than rebuilding a tree.
func listFixture(t *testing.T) *testRepo {
	t.Helper()

	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{
		"index.md":            []byte("---\ntitle: index\n---\n\nroot\n"),
		"README.MD":           []byte("---\ntitle: shouty\n---\n\nupper\n"),
		"posts/a.md":          []byte("---\ntitle: a\n---\n\na\n"),
		"posts/b.markdown":    []byte("---\ntitle: b\n---\n\nb\n"),
		"posts/nested/c.md":   []byte("---\ntitle: c\n---\n\nc\n"),
		"posts/image.txt":     []byte("not markdown\n"),
		"posts/notes.md.orig": []byte("a merge leftover\n"),
		"drafts/d.md":         []byte("---\ntitle: d\n---\n\nd\n"),
	})
	return repo
}

// paths is the listing reduced to what a caller would show in an index.
func paths(entries []core.ContentEntry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Path.String())
	}
	return out
}

// TestListReturnsMarkdownDocumentsSortedByPath is the shape core specifies:
// recursive, Markdown only, sorted.
func TestListReturnsMarkdownDocumentsSortedByPath(t *testing.T) {
	repo := listFixture(t)

	entries, err := repo.open().List(t.Context(), mustCollection(t, "posts"))
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	want := []string{"posts/a.md", "posts/b.markdown", "posts/nested/c.md"}
	if got := paths(entries); !slices.Equal(got, want) {
		t.Errorf("List = %v, want %v", got, want)
	}
	if !slices.IsSorted(paths(entries)) {
		t.Errorf("List = %v, which is not sorted by path", paths(entries))
	}
}

// TestListIsRecursive states the depth rule on its own, because a listing that
// stopped at one level would still pass a test that only looked at the top.
func TestListIsRecursive(t *testing.T) {
	repo := listFixture(t)

	entries, err := repo.open().List(t.Context(), mustCollection(t, "posts"))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !slices.Contains(paths(entries), "posts/nested/c.md") {
		t.Errorf("List = %v, want a document nested below the collection in it", paths(entries))
	}
}

// TestListIgnoresNonMarkdownFiles leans on core.ContentPath.IsMarkdown rather
// than on a suffix check of this package's own, which is why ".MD" is asserted:
// core compares case-insensitively, and an adapter doing its own comparison
// would get that wrong.
func TestListIgnoresNonMarkdownFiles(t *testing.T) {
	repo := listFixture(t)

	entries, err := repo.open().List(t.Context(), core.Collection{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := paths(entries)

	for _, unwanted := range []string{"posts/image.txt", "posts/notes.md.orig"} {
		if slices.Contains(got, unwanted) {
			t.Errorf("List = %v, want %q left out", got, unwanted)
		}
	}
	if !slices.Contains(got, "README.MD") {
		t.Errorf("List = %v, want %q in it: core.ContentPath.IsMarkdown is case-insensitive", got, "README.MD")
	}
}

// TestListRevisionsMatchGet is the consistency check between the two read paths.
// A revision scheme that drifted between the tree walk and the object read would
// show up here and nowhere else.
func TestListRevisionsMatchGet(t *testing.T) {
	repo := listFixture(t)
	opened := repo.open()

	entries, err := opened.List(t.Context(), core.Collection{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("List returned nothing; the check below would pass vacuously")
	}

	for _, entry := range entries {
		got, err := opened.Get(t.Context(), entry.Path)
		if err != nil {
			t.Errorf("Get %q: %v", entry.Path, err)
			continue
		}
		if got.Revision != entry.Revision {
			t.Errorf("%q: List says %q, Get says %q", entry.Path, entry.Revision, got.Revision)
		}
		if want := core.Revision(repo.blobAt(entry.Path.String())); entry.Revision != want {
			t.Errorf("%q: List says %q, git says %q", entry.Path, entry.Revision, want)
		}
	}
}

// TestListOfAnEmptyCollectionIsEmptyAndNotAnError is core's rule that a
// collection is a prefix rather than an object.
func TestListOfAnEmptyCollectionIsEmptyAndNotAnError(t *testing.T) {
	repo := listFixture(t)

	entries, err := repo.open().List(t.Context(), mustCollection(t, "nothing/here"))
	if err != nil {
		t.Fatalf("List of an empty collection: %v", err)
	}
	if entries == nil {
		t.Errorf("List = nil, want an empty slice")
	}
	if len(entries) != 0 {
		t.Errorf("List = %v, want no entries", paths(entries))
	}
}

// TestListOfTheRootCollectionReturnsEverything checks the zero Collection, which
// core made the root precisely so that "everything" needs no special constructor.
func TestListOfTheRootCollectionReturnsEverything(t *testing.T) {
	repo := listFixture(t)

	entries, err := repo.open().List(t.Context(), core.Collection{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	want := []string{"README.MD", "drafts/d.md", "index.md", "posts/a.md", "posts/b.markdown", "posts/nested/c.md"}
	if got := paths(entries); !slices.Equal(got, want) {
		t.Errorf("List = %v, want %v", got, want)
	}
}

// TestListOnAnUnbornBranchIsEmpty is the same prefix rule at the root of a
// repository with no commits: empty and nil, never ErrNotFound.
func TestListOnAnUnbornBranchIsEmpty(t *testing.T) {
	repo := newTestRepo(t)

	entries, err := repo.open().List(t.Context(), core.Collection{})
	if err != nil {
		t.Fatalf("List on an unborn branch: %v", err)
	}
	if entries == nil || len(entries) != 0 {
		t.Errorf("List = %v, want an empty slice and a nil error", entries)
	}
}

// TestListDoesNotDecodeDocuments is the counterpart to Get's refusal of a
// damaged file, and the pair pins the asymmetry.
//
// List promises only enough to draw an index, so it must not decode -- and
// therefore must not fail on content it never looked at. A damaged document that
// vanished from the listing would be one nobody could find in order to fix.
func TestListDoesNotDecodeDocuments(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{
		"posts/broken.md": []byte("---\ntitle: never closed\n"),
		"posts/fine.md":   []byte("---\ntitle: fine\n---\n\nok\n"),
	})
	opened := repo.open()

	entries, err := opened.List(t.Context(), core.Collection{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := paths(entries); !slices.Equal(got, []string{"posts/broken.md", "posts/fine.md"}) {
		t.Fatalf("List = %v, want the damaged document listed too", got)
	}
	if entries[0].Revision != core.Revision(repo.blobAt("posts/broken.md")) {
		t.Errorf("the damaged document has revision %q, want %q", entries[0].Revision, repo.blobAt("posts/broken.md"))
	}

	// ... and Get still refuses it, which is the half of the asymmetry that makes
	// listing it safe.
	if _, err := opened.Get(t.Context(), mustPath(t, "posts/broken.md")); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("Get of the damaged document: got %v, want an error matching core.ErrInvalid", err)
	}
}

// TestListHonoursContentRoot checks that a listing is bounded by the root and
// reports paths relative to it, so that what List returns can be passed straight
// back to Get.
func TestListHonoursContentRoot(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{
		"content/posts/a.md": []byte("---\ntitle: a\n---\n\na\n"),
		"content/index.md":   []byte("---\ntitle: i\n---\n\ni\n"),
		"secret/b.md":        []byte("---\ntitle: b\n---\n\nb\n"),
		"README.md":          []byte("---\ntitle: r\n---\n\nr\n"),
	})
	opened := repo.open(WithContentRoot(mustCollection(t, "content")))

	entries, err := opened.List(t.Context(), core.Collection{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := paths(entries); !slices.Equal(got, []string{"index.md", "posts/a.md"}) {
		t.Fatalf("List = %v, want only the documents under the content root, named relative to it", got)
	}
	if _, err := opened.Get(t.Context(), entries[0].Path); err != nil {
		t.Errorf("Get of a listed path: %v; a listing must be usable as addressing", err)
	}

	entries, err = opened.List(t.Context(), mustCollection(t, "posts"))
	if err != nil {
		t.Fatalf("List of a collection under the root: %v", err)
	}
	if got := paths(entries); !slices.Equal(got, []string{"posts/a.md"}) {
		t.Errorf("List = %v, want %v", got, []string{"posts/a.md"})
	}
}

// TestListSkipsPathsCoreCannotRepresent records the decision rather than leaving
// it to be discovered.
//
// Git permits filenames core does not, and an entry a caller cannot pass back to
// Get is worse than no entry: it is a row in an admin UI that errors on every
// click. Failing the whole listing would be worse still -- one such file,
// committed by anyone, ever, would make the collection unlistable.
func TestListSkipsPathsCoreCannotRepresent(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{
		"posts/fine.md":       []byte("---\ntitle: fine\n---\n\nok\n"),
		`posts/back\slash.md`: []byte("---\ntitle: no\n---\n\nno\n"),
	})

	if _, err := core.NewContentPath(`posts/back\slash.md`); err == nil {
		t.Fatal("core now accepts a backslash, so this test no longer records what it claims to")
	}

	entries, err := repo.open().List(t.Context(), core.Collection{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := paths(entries); !slices.Equal(got, []string{"posts/fine.md"}) {
		t.Errorf("List = %v, want the unrepresentable path skipped and the listing still returned", got)
	}
}

// TestListSkipsEntriesThatAreNotRegularFiles keeps the listing to things that
// are documents.
//
// A submodule is a "commit" entry and a symbolic link is a blob holding a target
// path; neither is content a caller could Get, and presenting a link as a
// document would invite a Put that silently turned it into a regular file.
func TestListSkipsEntriesThatAreNotRegularFiles(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"posts/fine.md": []byte("---\ntitle: fine\n---\n\nok\n")})
	repo.commitModes("add a link and a submodule", map[string]modeEntry{
		"posts/link.md":   {mode: "120000", content: []byte("../elsewhere.md")},
		"posts/module.md": {mode: "160000", oid: repo.head()},
	})

	entries, err := repo.open().List(t.Context(), core.Collection{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := paths(entries); !slices.Equal(got, []string{"posts/fine.md"}) {
		t.Errorf("List = %v, want only the regular file", got)
	}
}

// TestListHonoursContextCancellation is core's rule for every implementation.
func TestListHonoursContextCancellation(t *testing.T) {
	repo := listFixture(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := repo.open().List(ctx, core.Collection{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("List with a cancelled context: got %v, want an error matching context.Canceled", err)
	}
}

// TestListRejectsACollectionThatLooksLikeAnOption is the argv rule at the one
// place a caller-supplied directory reaches an argument vector.
func TestListRejectsACollectionThatLooksLikeAnOption(t *testing.T) {
	repo := listFixture(t)

	weird, err := core.NewCollection("-x")
	if err != nil {
		t.Fatalf("core.NewCollection(%q) = %v; core now rejects it, so this test no longer records the exposure it was written for", "-x", err)
	}
	if _, err := repo.open().List(t.Context(), weird); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("List of a collection that looks like an option: got %v, want an error matching core.ErrInvalid", err)
	}
}
