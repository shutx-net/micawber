package git

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shutx-net/micawber/core"
)

// TestGetReturnsTheDocumentAndItsBlobRevision is where the answer to phase 1's
// open question is written down: a Revision is the blob object id.
//
// It is asserted against git's own answer rather than against a constant, so the
// test states the decision instead of hard-coding a hash that would have to be
// updated whenever the fixture changed.
func TestGetReturnsTheDocumentAndItsBlobRevision(t *testing.T) {
	repo := newTestRepo(t)
	stored := []byte("---\ntitle: Hello\ndraft: false\n---\n\n# Hello\n\nBody.\n")
	repo.commit("first", map[string][]byte{"posts/hello.md": stored})

	got, err := repo.open().Get(t.Context(), mustPath(t, "posts/hello.md"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.Path.String() != "posts/hello.md" {
		t.Errorf("Path = %q, want %q", got.Path, "posts/hello.md")
	}
	if want := core.Revision(repo.blobAt("posts/hello.md")); got.Revision != want {
		t.Errorf("Revision = %q, want the blob object id %q", got.Revision, want)
	}
	if got.FrontMatter.Format != core.FrontMatterYAML {
		t.Errorf("FrontMatter.Format = %q, want %q", got.FrontMatter.Format, core.FrontMatterYAML)
	}
	if title, _ := got.FrontMatter.Text("title"); title != "Hello" {
		t.Errorf("front matter title = %q, want %q", title, "Hello")
	}
	if want := []byte("\n# Hello\n\nBody.\n"); !bytes.Equal(got.Body, want) {
		t.Errorf("Body = %q, want %q", got.Body, want)
	}
}

// TestGetIsByteExactForCRLFAndBOM keeps the byte-level contract markdown built
// intact across the repository boundary: what was committed is what comes back,
// delimiter bytes and all.
func TestGetIsByteExactForCRLFAndBOM(t *testing.T) {
	cases := []struct {
		name     string
		stored   []byte
		wantRaw  []byte
		wantBody []byte
	}{
		{"crlf.md", []byte("---\r\ntitle: a\r\n---\r\n\r\nbody\r\n"), []byte("title: a\r\n"), []byte("\r\nbody\r\n")},
		{"bom.md", []byte("\ufeff---\ntitle: a\n---\n\nbody\n"), []byte("title: a\n"), []byte("\nbody\n")},
		{"tabs.md", []byte("---  \ntitle: a\n---\t\n\nbody\n"), []byte("title: a\n"), []byte("\nbody\n")},
		{"eof.md", []byte("---\ntitle: a\n---"), []byte("title: a\n"), []byte("")},
	}

	repo := newTestRepo(t)
	files := map[string][]byte{}
	for _, c := range cases {
		files[c.name] = c.stored
	}
	repo.commit("first", files)
	opened := repo.open()

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := opened.Get(t.Context(), mustPath(t, c.name))
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if !bytes.Equal(got.FrontMatter.Raw, c.wantRaw) {
				t.Errorf("FrontMatter.Raw = %q, want %q", got.FrontMatter.Raw, c.wantRaw)
			}
			if !bytes.Equal(got.Body, c.wantBody) {
				t.Errorf("Body = %q, want %q", got.Body, c.wantBody)
			}
			// The bytes git holds are what was committed: no filter ran on the
			// way in either.
			if !bytes.Equal(repo.blob(c.name), c.stored) {
				t.Errorf("the stored blob is %q, want %q", repo.blob(c.name), c.stored)
			}
		})
	}
}

// TestGetIsUnaffectedByAutocrlfAndGitattributes is the test the object-layer
// design exists for.
//
// With core.autocrlf on and a .gitattributes demanding CRLF, any implementation
// that read through a working tree would hand back rewritten bytes. Reading blobs
// straight out of the object database makes the guarantee Micawber's rather than
// the user's configuration's.
func TestGetIsUnaffectedByAutocrlfAndGitattributes(t *testing.T) {
	repo := newTestRepo(t)
	repo.git(nil, nil, "config", "core.autocrlf", "true")
	stored := []byte("---\ntitle: a\n---\n\nline one\nline two\n")
	repo.commit("first", map[string][]byte{
		".gitattributes": []byte("*.md text eol=crlf\n"),
		"posts/a.md":     stored,
	})

	got, err := repo.open().Get(t.Context(), mustPath(t, "posts/a.md"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if bytes.Contains(got.Body, []byte("\r\n")) {
		t.Errorf("Body = %q; a line-ending filter reached the bytes", got.Body)
	}
	if want := []byte("\nline one\nline two\n"); !bytes.Equal(got.Body, want) {
		t.Errorf("Body = %q, want %q", got.Body, want)
	}
}

// TestGetOfAMissingPathIsErrNotFound covers absence, which cat-file reports as a
// structured "missing" line rather than as an exit status or a message.
func TestGetOfAMissingPathIsErrNotFound(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"posts/a.md": []byte("hi\n")})

	_, err := repo.open().Get(t.Context(), mustPath(t, "posts/nope.md"))
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Get of a missing path: got %v, want an error matching core.ErrNotFound", err)
	}
}

// TestGetOfADirectoryIsErrNotFound records the mapping: a tree is not a
// document, and core has no more accurate vocabulary than "there is no document
// here".
func TestGetOfADirectoryIsErrNotFound(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"posts/a.md": []byte("hi\n")})

	_, err := repo.open().Get(t.Context(), mustPath(t, "posts"))
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Get of a directory: got %v, want an error matching core.ErrNotFound", err)
	}
}

// TestGetOnAnUnbornBranchIsErrNotFound covers a repository somebody has only run
// git init in, which is how every repository starts.
func TestGetOnAnUnbornBranchIsErrNotFound(t *testing.T) {
	repo := newTestRepo(t)

	_, err := repo.open().Get(t.Context(), mustPath(t, "posts/a.md"))
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Get on an unborn branch: got %v, want an error matching core.ErrNotFound", err)
	}
}

// TestGetOfADamagedDocumentIsErrInvalid passes markdown's refusal straight
// through, and checks that the refusal carries none of the document with it.
//
// A front-matter block may hold an API token. markdown states that rule for its
// own errors; an adapter that helpfully quoted the file in its wrapper would
// undo it.
func TestGetOfADamagedDocumentIsErrInvalid(t *testing.T) {
	repo := newTestRepo(t)
	secret := "ghp_000000000000000000000000000000000000"
	body := "wenzelsdorf-marginalia"
	repo.commit("first", map[string][]byte{
		"posts/broken.md": []byte("---\ntoken: " + secret + "\n" + body + "\n"),
	})

	_, err := repo.open().Get(t.Context(), mustPath(t, "posts/broken.md"))
	if !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Get of a damaged document: got %v, want an error matching core.ErrInvalid", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), body) {
		t.Errorf("Error() = %q; it carries the document's bytes", err.Error())
	}
}

// TestGetHonoursContentRoot checks that the root bounds addressing in both
// directions: a path is joined onto it, and a document outside it is simply not
// there.
func TestGetHonoursContentRoot(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{
		"content/posts/a.md": []byte("---\ntitle: inside\n---\n\nyes\n"),
		"secret/b.md":        []byte("---\ntitle: outside\n---\n\nno\n"),
	})
	opened := repo.open(WithContentRoot(mustCollection(t, "content")))

	got, err := opened.Get(t.Context(), mustPath(t, "posts/a.md"))
	if err != nil {
		t.Fatalf("Get within the content root: %v", err)
	}
	if title, _ := got.FrontMatter.Text("title"); title != "inside" {
		t.Errorf("title = %q, want %q", title, "inside")
	}
	if want := core.Revision(repo.blobAt("content/posts/a.md")); got.Revision != want {
		t.Errorf("Revision = %q, want %q", got.Revision, want)
	}

	if _, err := opened.Get(t.Context(), mustPath(t, "secret/b.md")); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Get of a path outside the content root: got %v, want an error matching core.ErrNotFound", err)
	}
}

// TestGetHonoursContextCancellation is the contract core states for every
// implementation: honour ctx and return its error.
func TestGetHonoursContextCancellation(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"posts/a.md": []byte("---\ntitle: a\n---\n\nhi\n")})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := repo.open().Get(ctx, mustPath(t, "posts/a.md")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get with a cancelled context: got %v, want an error matching context.Canceled", err)
	}
}

// TestGetReturnsContentTheCallerMayMutate is core's rule that returned values
// belong to the caller.
//
// markdown.Parse aliases the buffer it is handed rather than copying, so this is
// worth a test: it holds because the buffer is a fresh read the package does not
// keep, and the next person to add a read cache will need to know that is the
// reason a cache would have to Clone.
func TestGetReturnsContentTheCallerMayMutate(t *testing.T) {
	repo := newTestRepo(t)
	stored := []byte("---\ntitle: a\n---\n\nbody\n")
	repo.commit("first", map[string][]byte{"posts/a.md": stored})
	opened := repo.open()
	p := mustPath(t, "posts/a.md")

	first, err := opened.Get(t.Context(), p)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	for i := range first.Body {
		first.Body[i] = 'X'
	}
	for i := range first.FrontMatter.Raw {
		first.FrontMatter.Raw[i] = 'X'
	}
	first.FrontMatter.Fields["title"] = "clobbered"

	second, err := opened.Get(t.Context(), p)
	if err != nil {
		t.Fatalf("Get again: %v", err)
	}
	if want := []byte("\nbody\n"); !bytes.Equal(second.Body, want) {
		t.Errorf("Body = %q, want %q; the two reads share state", second.Body, want)
	}
	if title, _ := second.FrontMatter.Text("title"); title != "a" {
		t.Errorf("title = %q, want %q; the two reads share state", title, "a")
	}
}

// TestGetOfASubmoduleIsErrNotFoundAndOfALinkIsItsTarget records where the two
// read paths deliberately disagree, so that it is a decision rather than a
// discovery.
//
// A submodule is a "commit" object, which cat-file reports as such, so Get can
// refuse it in the one process it costs. A symbolic link is a blob holding a path,
// indistinguishable from a document without also reading the tree entry -- so Get
// returns its bytes, while List, which is reading tree entries anyway, leaves it
// out. A caller that only ever addresses what a listing gave it never meets
// either.
func TestGetOfASubmoduleIsErrNotFoundAndOfALinkIsItsTarget(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"posts/fine.md": []byte("---\ntitle: fine\n---\n\nok\n")})
	repo.commitModes("add a link and a submodule", map[string]modeEntry{
		"posts/link.md":   {mode: "120000", content: []byte("../elsewhere.md")},
		"posts/module.md": {mode: "160000", oid: repo.head()},
	})
	opened := repo.open()

	if _, err := opened.Get(t.Context(), mustPath(t, "posts/module.md")); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Get of a submodule: got %v, want an error matching core.ErrNotFound", err)
	}

	link, err := opened.Get(t.Context(), mustPath(t, "posts/link.md"))
	if err != nil {
		t.Fatalf("Get of a symbolic link: %v", err)
	}
	if string(link.Body) != "../elsewhere.md" {
		t.Errorf("Get of a symbolic link returned %q, want the link's target text; if this has changed, git/doc.go is now wrong", link.Body)
	}
	if !link.FrontMatter.IsZero() {
		t.Errorf("the link's target parsed as front matter: %v", link.FrontMatter)
	}

	entries, err := opened.List(t.Context(), core.Collection{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if containsPath(entries, "posts/link.md") || containsPath(entries, "posts/module.md") {
		t.Errorf("List = %v, want neither the link nor the submodule", paths(entries))
	}
}
