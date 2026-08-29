package git

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/shutx-net/micawber/core"
)

// packageFlags is every argument beginning with a dash that this package is
// allowed to pass to git: literals it wrote itself, and nothing else.
//
// The guard below checks every argument vector actually executed against this
// list, so an operand that got read as an option -- a document called "-rf.md"
// interpolated into a command -- fails as an unexpected flag rather than as a
// surprising outcome. An outcome-only test would pass for the wrong reason.
var packageFlags = map[string]bool{
	"--":                    true,
	"--batch":               true,
	"--empty":               true,
	"--full-tree":           true,
	"--git-dir":             true,
	"--index-info":          true,
	"--no-abbrev":           true,
	"--no-follow":           true,
	"--quiet":               true,
	"--stdin":               true,
	"--verify":              true,
	"-F":                    true,
	"-n":                    true,
	"-p":                    true,
	"-r":                    true,
	"-w":                    true,
	"-z":                    true,
	"--format=" + logFormat: true,
}

// recorder collects every argument vector a Repository executed.
type recorder struct {
	mu    sync.Mutex
	calls [][]string
}

// watch installs the recorder on a repository and returns it.
func watch(repo *Repository) *recorder {
	rec := &recorder{}
	repo.exec.observe = func(args []string) {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		rec.calls = append(rec.calls, append([]string(nil), args...))
	}
	return rec
}

// unexpectedFlags returns every dash-leading argument that this package did not
// write as a literal.
func (rec *recorder) unexpectedFlags() []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()

	var found []string
	for _, call := range rec.calls {
		for i, arg := range call {
			if !strings.HasPrefix(arg, "-") || packageFlags[arg] {
				continue
			}
			// A bare "-" is commit-tree's "read the message from standard
			// input", and is allowed only in that one position. core accepts "-"
			// as a path too, so waving it through everywhere would blunt this
			// guard exactly where it is meant to be sharp.
			if arg == "-" && i > 0 && call[i-1] == "-F" {
				continue
			}
			found = append(found, arg)
		}
	}
	return found
}

// TestPathsBeginningWithADashNeverReachAnArgumentVector is the argument-injection
// sweep, run over every operation.
//
// core accepts all of these, and correctly so: none is a traversal, and each is a
// legal filename on POSIX and a legal path in Git. They are hazardous only to
// something that builds argument vectors, so the rule lives at the argv boundary:
// paths travel on standard input where a plumbing command takes them there, and
// are wrapped as an explicit pathspec where they must be arguments. Either the
// operation works or it is refused with ErrInvalid; what it must never do is run
// a differently shaped command.
func TestPathsBeginningWithADashNeverReachAnArgumentVector(t *testing.T) {
	hazards := []string{"-oevil.md", "--upload-pack=x.md", "-rf.md", "posts/--help.md", "a/-b.md"}

	repo := newTestRepo(t)
	opened := repo.open()
	rec := watch(opened)

	for _, hazard := range hazards {
		t.Run(hazard, func(t *testing.T) {
			p, err := core.NewContentPath(hazard)
			if err != nil {
				t.Fatalf("core.NewContentPath(%q) = %v; core now rejects it, so this test no longer records the exposure it was written for", hazard, err)
			}

			c := core.Content{
				Path:        p,
				FrontMatter: core.FrontMatter{Format: core.FrontMatterYAML, Fields: map[string]any{"title": "hazard"}},
				Body:        []byte("\nhazard\n"),
			}
			rev, err := opened.Put(t.Context(), c, testChange("create "+hazard))
			if err != nil {
				requireInvalid(t, "Put", err)
				return
			}

			// It worked, so it must have worked properly: the document is where
			// it says it is, with the bytes it was given.
			if want := core.Revision(repo.blobAt(hazard)); rev != want {
				t.Errorf("Put returned %q, want %q", rev, want)
			}
			got, err := opened.Get(t.Context(), p)
			if err != nil {
				requireInvalid(t, "Get", err)
			} else if string(got.Body) != "\nhazard\n" {
				t.Errorf("Get returned body %q, want %q", got.Body, "\nhazard\n")
			}

			entries, err := opened.List(t.Context(), core.Collection{})
			if err != nil {
				t.Errorf("List: %v", err)
			} else if !containsPath(entries, hazard) {
				t.Errorf("List = %v, want %q in it", paths(entries), hazard)
			}

			if _, err := opened.History(t.Context(), p, 0); err != nil {
				requireInvalid(t, "History", err)
			}
			if err := opened.Delete(t.Context(), p, rev, testChange("delete "+hazard)); err != nil {
				requireInvalid(t, "Delete", err)
			}
		})
	}

	if unexpected := rec.unexpectedFlags(); len(unexpected) > 0 {
		t.Errorf("git was passed %v, which this package did not write as literal flags; a path was read as an option", unexpected)
	}
	if len(rec.calls) == 0 {
		t.Fatal("no git command was recorded; the guard would pass vacuously")
	}
}

// requireInvalid fails unless err is the one refusal the argv rule is allowed to
// produce.
func requireInvalid(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, core.ErrInvalid) {
		t.Errorf("%s: got %v, want either success or an error matching core.ErrInvalid", what, err)
	}
}

// TestAuthorAndMessageCannotBecomeArguments records why the other two
// caller-supplied strings are safe, rather than leaving it to be rediscovered.
//
// An author travels in the environment and a message on standard input, so
// neither can be read as an option however it is spelled -- and both are recorded
// verbatim, which a defensive rewrite would have broken.
func TestAuthorAndMessageCannotBecomeArguments(t *testing.T) {
	repo := newTestRepo(t)
	opened := repo.open()
	rec := watch(opened)

	change := core.Change{
		Message: "--upload-pack=touch /tmp/pwned\n\n-rf --all",
		Author:  core.Author{Name: "--exec=nope", Email: "-x@example.invalid"},
		Time:    fixtureTime(),
	}
	if _, err := opened.Put(t.Context(), newContent(t, "a.md", "a", "\na\n"), change); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if got := repo.commitField("%an"); got != change.Author.Name {
		t.Errorf("commit author = %q, want %q recorded verbatim", got, change.Author.Name)
	}
	if got := repo.commitField("%ae"); got != change.Author.Email {
		t.Errorf("commit author e-mail = %q, want %q recorded verbatim", got, change.Author.Email)
	}
	if got := repo.commitField("%B"); strings.TrimRight(got, "\n") != strings.TrimRight(change.Message, "\n") {
		t.Errorf("commit message = %q, want %q recorded verbatim", got, change.Message)
	}
	if unexpected := rec.unexpectedFlags(); len(unexpected) > 0 {
		t.Errorf("git was passed %v; an author or a message reached an argument vector", unexpected)
	}
}

// TestErrorsCarryNoDocumentBytesAndNoEnvironment is the rule markdown already
// states, held to across this package's own wrapping.
//
// A front-matter block may hold an API token, and the environment is where a
// credential helper's configuration would be. Neither belongs in an error a
// server will log or hand to an API client.
func TestErrorsCarryNoDocumentBytesAndNoEnvironment(t *testing.T) {
	const token = "ghp_222222222222222222222222222222222222"
	const secretEnv = "MICAWBER_TEST_CREDENTIAL"
	const secretValue = "s3cr3t-credential-value"
	t.Setenv(secretEnv, secretValue)

	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{
		"posts/broken.md": []byte("---\ntoken: " + token + "\nunterminated\n"),
		"posts/fine.md":   []byte("---\ntitle: fine\n---\n\nfine\n"),
	})
	opened := repo.open()

	damagedRev := core.Revision(repo.blobAt("posts/broken.md"))
	fine, err := opened.Get(t.Context(), mustPath(t, "posts/fine.md"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	stale := fine
	stale.Revision = "0000000000000000000000000000000000000000"
	stale.FrontMatter.Fields["token"] = token

	var errs []error
	collect := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	_, err = opened.Get(t.Context(), mustPath(t, "posts/broken.md"))
	collect(err)
	_, err = opened.GetRevision(t.Context(), mustPath(t, "posts/fine.md"), damagedRev)
	collect(err)
	_, err = opened.Put(t.Context(), stale, testChange("conflicting write"))
	collect(err)
	_, err = opened.Get(t.Context(), mustPath(t, "posts/absent.md"))
	collect(err)
	collect(opened.Delete(t.Context(), mustPath(t, "posts/absent.md"), "", testChange("remove nothing")))
	_, err = opened.History(t.Context(), mustPath(t, "posts/absent.md"), 0)
	collect(err)

	if len(errs) < 6 {
		t.Fatalf("provoked only %d errors, want 6; the sweep is not covering what it claims to", len(errs))
	}
	for _, err := range errs {
		message := err.Error()
		if strings.Contains(message, token) {
			t.Errorf("error %q carries a front-matter value", message)
		}
		if strings.Contains(message, secretValue) {
			t.Errorf("error %q carries an environment value", message)
		}
		if strings.Contains(message, "unterminated") {
			t.Errorf("error %q carries the document's bytes", message)
		}
	}

	// And the environment really was there to leak, so the check is not passing
	// because there was nothing to find.
	if os.Getenv(secretEnv) != secretValue {
		t.Fatal("the fixture credential is not in the environment, so this test proves nothing")
	}
}

// TestContentRootCannotBeEscaped and its companion below are two halves of one
// claim: a path cannot address anything outside the root, and it cannot because
// of how the values are built rather than because of a check here.
func TestContentRootCannotBeEscaped(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{
		"content/posts/a.md": []byte("---\ntitle: a\n---\n\na\n"),
		"secret/private.md":  []byte("---\ntitle: private\n---\n\nprivate\n"),
		"outside.md":         []byte("---\ntitle: outside\n---\n\noutside\n"),
	})
	opened := repo.open(WithContentRoot(mustCollection(t, "content")))
	before := repo.head()

	entries, err := opened.List(t.Context(), core.Collection{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Path.String() != "posts/a.md" {
		t.Fatalf("List = %v, want only the document inside the root", paths(entries))
	}

	for _, outside := range []string{"secret/private.md", "outside.md"} {
		p := mustPath(t, outside)
		if _, err := opened.Get(t.Context(), p); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("Get(%q): got %v, want an error matching core.ErrNotFound", outside, err)
		}
		if _, err := opened.History(t.Context(), p, 0); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("History(%q): got %v, want an error matching core.ErrNotFound", outside, err)
		}
		if err := opened.Delete(t.Context(), p, "", testChange("remove")); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("Delete(%q): got %v, want an error matching core.ErrNotFound", outside, err)
		}
	}
	if repo.head() != before {
		t.Error("the branch moved while nothing was supposed to change")
	}
	if repo.blobAt("secret/private.md") == "" || repo.blobAt("outside.md") == "" {
		t.Error("a document outside the content root was touched")
	}
}

// TestPutRejectsAPathOutsideTheContentRootByConstruction states where the
// guarantee actually comes from.
//
// It is not a check this package makes and could forget: a ContentPath cannot
// hold a ".." segment at all, and Collection.Join validates the relative part
// before joining it, so there is no value that names something outside the root
// for the adapter to be careful with.
func TestPutRejectsAPathOutsideTheContentRootByConstruction(t *testing.T) {
	root := mustCollection(t, "content")

	for _, escape := range []string{"../secret.md", "a/../../secret.md", "/etc/passwd", "../../.git/config"} {
		if _, err := core.NewContentPath(escape); err == nil {
			t.Errorf("core.NewContentPath(%q) succeeded; the guarantee this package relies on is gone", escape)
		}
		if _, err := root.Join(escape); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("Collection.Join(%q) = %v, want an error matching core.ErrInvalid", escape, err)
		}
	}

	// The zero path has no address at all, and the adapter must not turn it into
	// the content root itself.
	repo := newTestRepo(t)
	opened := repo.open(WithContentRoot(root))
	if _, err := opened.Get(t.Context(), core.ContentPath{}); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("Get of the zero path: got %v, want an error matching core.ErrInvalid", err)
	}
}
