package git

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shutx-net/micawber/core"
)

// The fixed identity and clock every fixture commit is made with. Nothing here
// is a real address: tests must not look like they might reach somebody.
//
// Fixing all six of GIT_AUTHOR_* and GIT_COMMITTER_* is what makes a fixture
// commit byte-identical between runs and between machines, which in turn lets a
// test assert an exact object id where that is the clearest assertion.
const (
	fixtureName  = "Fixture Author"
	fixtureEmail = "fixture@example.invalid"
	fixtureDate  = "2001-02-03T04:05:06+00:00"
)

// gitAbsent records why git could not be used, and is empty when it can. Tests
// that need a repository skip on it; the architecture guards do not, because
// they only read source.
var gitAbsent string

// TestMain scrubs the environment once for the whole package and decides whether
// git is usable at all.
//
// The scrub is what makes every test in this package hermetic: with
// GIT_CONFIG_GLOBAL, GIT_CONFIG_SYSTEM and GIT_CONFIG_NOSYSTEM set, no
// developer's own git configuration -- commit.gpgsign, core.autocrlf,
// init.defaultBranch, a hooks path -- can reach a fixture or the adapter, since
// the adapter builds its environment from this process's.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "micawber-git-home")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create fixture HOME: %v\n", err)
		os.Exit(1)
	}

	for name, value := range map[string]string{
		"HOME":                home,
		"XDG_CONFIG_HOME":     filepath.Join(home, "config"),
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_CONFIG_SYSTEM":   os.DevNull,
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_TERMINAL_PROMPT": "0",
		"GIT_AUTHOR_NAME":     fixtureName,
		"GIT_AUTHOR_EMAIL":    fixtureEmail,
		"GIT_AUTHOR_DATE":     fixtureDate,
		"GIT_COMMITTER_NAME":  fixtureName,
		"GIT_COMMITTER_EMAIL": fixtureEmail,
		"GIT_COMMITTER_DATE":  fixtureDate,
	} {
		if err := os.Setenv(name, value); err != nil {
			fmt.Fprintf(os.Stderr, "set %s: %v\n", name, err)
			os.Exit(1)
		}
	}
	// A GIT_DIR inherited from whatever ran the tests -- a git hook, say -- would
	// silently redirect every fixture command away from its own repository.
	for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE"} {
		if err := os.Unsetenv(name); err != nil {
			fmt.Fprintf(os.Stderr, "unset %s: %v\n", name, err)
			os.Exit(1)
		}
	}

	if out, err := exec.Command("git", "--version").CombinedOutput(); err != nil {
		gitAbsent = fmt.Sprintf("git is not usable (%v: %s); this package drives the git binary, so its repository tests cannot run", err, bytes.TrimSpace(out))
	}

	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}

// testRepo is a throwaway Git repository under t.TempDir(), built and inspected
// with plain git so that assertions about what the adapter wrote are made by
// something other than the adapter.
type testRepo struct {
	t   *testing.T
	dir string
	ref string
}

// newTestRepo initialises an empty repository on an unborn branch named main.
// It skips the test when git is absent rather than failing it.
func newTestRepo(t *testing.T) *testRepo {
	t.Helper()
	if gitAbsent != "" {
		t.Skip(gitAbsent)
	}

	dir := t.TempDir()
	r := &testRepo{t: t, dir: dir, ref: "refs/heads/main"}
	r.git(nil, nil, "init", "-q", "-b", "main")
	return r
}

// git runs one git command in the repository and fails the test if it does not
// succeed. env holds additional environment entries and stdin may be nil.
//
// It uses os/exec directly rather than the adapter's runner, so that a fixture
// is never built by the code under test.
func (r *testRepo) git(env []string, stdin []byte, args ...string) string {
	r.t.Helper()
	out, err := r.tryGit(env, stdin, args...)
	if err != nil {
		r.t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return out
}

// tryGit is git without the fatal, for the cases where a non-zero exit is the
// answer the caller wants.
func (r *testRepo) tryGit(env []string, stdin []byte, args ...string) (string, error) {
	r.t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// modeEntry is one tree entry staged by hand: its file mode, and either the
// bytes to hash into a blob or an object id to point at directly. It exists so
// that a fixture can hold a symbolic link or a submodule, which the adapter has
// to skip and which no ordinary write would produce.
type modeEntry struct {
	mode    string
	content []byte
	oid     string
}

// fixtureCommit describes one commit to add to the fixture's branch. Zero-valued
// identity fields fall back to the package's fixed author and clock.
type fixtureCommit struct {
	message string
	name    string
	email   string
	date    string
	entries map[string]modeEntry
	removed []string
}

// commit adds message and files as one commit on the fixture's branch, using the
// fixed identity, and returns the new commit's object id.
func (r *testRepo) commit(message string, files map[string][]byte) string {
	r.t.Helper()
	return r.commitFixture(fixtureCommit{message: message, entries: regularFiles(files)})
}

// commitModes is commit for entries that are not regular files.
func (r *testRepo) commitModes(message string, entries map[string]modeEntry) string {
	r.t.Helper()
	return r.commitFixture(fixtureCommit{message: message, entries: entries})
}

// remove commits the deletion of paths.
func (r *testRepo) remove(message string, paths ...string) string {
	r.t.Helper()
	return r.commitFixture(fixtureCommit{message: message, removed: paths})
}

// regularFiles turns a path-to-bytes map into ordinary 100644 tree entries.
func regularFiles(files map[string][]byte) map[string]modeEntry {
	entries := make(map[string]modeEntry, len(files))
	for path, content := range files {
		entries[path] = modeEntry{mode: "100644", content: content}
	}
	return entries
}

// commitFixture writes c as a commit through a scratch index, exactly as the
// adapter does, so that building a fixture never touches a working tree and the
// repository can stay bare-shaped until a test asks for a checkout.
func (r *testRepo) commitFixture(c fixtureCommit) string {
	r.t.Helper()

	env := []string{
		"GIT_INDEX_FILE=" + filepath.Join(r.t.TempDir(), "index"),
		"GIT_AUTHOR_NAME=" + orDefault(c.name, fixtureName),
		"GIT_AUTHOR_EMAIL=" + orDefault(c.email, fixtureEmail),
		"GIT_AUTHOR_DATE=" + orDefault(c.date, fixtureDate),
		"GIT_COMMITTER_NAME=" + orDefault(c.name, fixtureName),
		"GIT_COMMITTER_EMAIL=" + orDefault(c.email, fixtureEmail),
		"GIT_COMMITTER_DATE=" + orDefault(c.date, fixtureDate),
	}

	parent, born := r.tip()
	if born {
		r.git(env, nil, "read-tree", parent)
	} else {
		r.git(env, nil, "read-tree", "--empty")
	}

	var info bytes.Buffer
	for _, path := range slices.Sorted(maps.Keys(c.entries)) {
		entry := c.entries[path]
		oid := entry.oid
		if oid == "" {
			oid = strings.TrimSpace(r.git(env, entry.content, "hash-object", "-w", "--stdin"))
		}
		fmt.Fprintf(&info, "%s %s\t%s\x00", entry.mode, oid, path)
	}
	for _, path := range c.removed {
		fmt.Fprintf(&info, "0 %s\t%s\x00", strings.Repeat("0", 40), path)
	}
	if info.Len() > 0 {
		r.git(env, info.Bytes(), "update-index", "-z", "--index-info")
	}

	tree := strings.TrimSpace(r.git(env, nil, "write-tree"))
	args := []string{"commit-tree", tree}
	if born {
		args = append(args, "-p", parent)
	}
	args = append(args, "-F", "-")
	commit := strings.TrimSpace(r.git(env, []byte(c.message), args...))

	old := ""
	if born {
		old = parent
	}
	r.git(env, nil, "update-ref", "--", r.ref, commit, old)
	return commit
}

// head returns the commit the fixture's branch points at, failing when it has
// none.
func (r *testRepo) head() string {
	r.t.Helper()
	oid, born := r.tip()
	if !born {
		r.t.Fatalf("branch %s is unborn", r.ref)
	}
	return oid
}

// tip returns the commit the fixture's branch points at, and whether it has one.
func (r *testRepo) tip() (string, bool) {
	r.t.Helper()
	out, err := r.tryGit(nil, nil, "rev-parse", "--verify", "-q", r.ref)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(out), true
}

// open opens the repository through the adapter, failing the test if it cannot.
func (r *testRepo) open(opts ...Option) *Repository {
	r.t.Helper()
	repo, err := Open(context.Background(), r.dir, opts...)
	if err != nil {
		r.t.Fatalf("Open: %v", err)
	}
	return repo
}

// orDefault returns s unless it is empty, in which case it returns fallback.
func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// mustCollection is NewCollection for a collection the test knows is valid.
func mustCollection(t *testing.T, s string) core.Collection {
	t.Helper()
	c, err := core.NewCollection(s)
	if err != nil {
		t.Fatalf("NewCollection(%q): %v", s, err)
	}
	return c
}

// blobAt returns the object id git records for path at the branch tip, which is
// what the adapter's Revision must equal.
func (r *testRepo) blobAt(path string) string {
	r.t.Helper()
	return strings.TrimSpace(r.git(nil, nil, "rev-parse", r.ref+":"+path))
}

// blob returns the bytes git holds for path at the branch tip, read straight
// from the object database so that no filter can touch them.
func (r *testRepo) blob(path string) []byte {
	r.t.Helper()
	return []byte(r.git(nil, nil, "cat-file", "blob", r.ref+":"+path))
}

// mustPath is NewContentPath for a path the test knows is valid.
func mustPath(t *testing.T, s string) core.ContentPath {
	t.Helper()
	p, err := core.NewContentPath(s)
	if err != nil {
		t.Fatalf("NewContentPath(%q): %v", s, err)
	}
	return p
}

// commitCount is the number of commits reachable from the branch tip.
func (r *testRepo) commitCount() int {
	r.t.Helper()
	if _, born := r.tip(); !born {
		return 0
	}
	var n int
	if _, err := fmt.Sscanf(strings.TrimSpace(r.git(nil, nil, "rev-list", "--count", r.ref)), "%d", &n); err != nil {
		r.t.Fatalf("parse rev-list --count: %v", err)
	}
	return n
}

// checkout materialises a working tree and an index, which a repository built
// only through the object layer does not otherwise have. Tests that assert the
// adapter leaves a human's checkout alone need one to leave alone.
func (r *testRepo) checkout() {
	r.t.Helper()
	r.git(nil, nil, "checkout", "-q", strings.TrimPrefix(r.ref, "refs/heads/"))
}

// fsck fails the test if git considers the repository damaged.
func (r *testRepo) fsck() {
	r.t.Helper()
	r.git(nil, nil, "fsck", "--strict", "--no-progress")
}

// fixtureTime is fixtureDate as a time.Time, for a Change that must produce a
// commit with the fixture's clock.
func fixtureTime() time.Time {
	t, err := time.Parse(time.RFC3339, fixtureDate)
	if err != nil {
		panic("git: the fixture date does not parse: " + err.Error())
	}
	return t
}

// testChange is the Change an adapter write carries in these tests: the fixed
// author and the fixed clock, so a commit the adapter makes is as reproducible
// as one a fixture makes.
func testChange(message string) core.Change {
	return core.Change{
		Message: message,
		Author:  core.Author{Name: fixtureName, Email: fixtureEmail},
		Time:    fixtureTime(),
	}
}
