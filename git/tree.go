package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/shutx-net/micawber/core"
)

// regularFileMode is the mode every document this package writes is stored with.
// Micawber has no notion of an executable document, and preserving a mode it
// cannot express would mean reading the old entry to find out what it was.
const regularFileMode = "100644"

// edit is one change to make to a tree: a blob to place at a repository-relative
// path, or the removal of whatever is there.
//
// Writes take a slice of these even though every operation today produces
// exactly one. core already anticipates a batched write -- "a rename is a Get, a
// Put and a Delete at the caller until a batched write exists" -- and a
// one-edit signature would have to be widened the moment there is one.
type edit struct {
	// path is repository-relative, with the content root already joined on.
	path string
	// blob is the object to place at path; unused when remove is set.
	blob objectID
	// remove asks for the entry at path to be dropped from the tree.
	remove bool
}

// hashObject writes data into the object database as a blob and returns its id.
//
// No --path is given, so no clean filter runs and the bytes stored are exactly
// the bytes markdown produced -- verified against a repository with
// core.autocrlf on. That is the whole of what byte fidelity costs here.
func (r *Repository) hashObject(ctx context.Context, data []byte) (objectID, error) {
	if data == nil {
		data = []byte{}
	}
	out, err := r.exec.run(ctx, invocation{args: []string{"hash-object", "-w", "--stdin"}, stdin: data})
	if err != nil {
		return "", fmt.Errorf("git: write the document: %w", err)
	}
	oid := objectID(bytes.TrimSpace(out))
	if !oid.valid() {
		return "", fmt.Errorf("git: hash-object returned %q, which is not an object id", oid)
	}
	return oid, nil
}

// writeTree applies edits to the tree of parent and returns the new tree.
//
// The edits are staged in a scratch index in its own temporary directory, named
// by GIT_INDEX_FILE, so the user's .git/index is never opened -- and so two
// concurrent writes cannot collide on it either. update-index creates every
// intermediate tree a path needs, which is why nothing here has to walk the
// path, and the paths travel on standard input rather than in the argument
// vector.
func (r *Repository) writeTree(ctx context.Context, parent objectID, born bool, edits []edit) (objectID, error) {
	dir, err := os.MkdirTemp("", "micawber-git-index")
	if err != nil {
		return "", fmt.Errorf("git: create a scratch index: %w", err)
	}
	// A hard kill can still leave one of these behind. It is an ordinary file in
	// the OS temporary directory, referenced by nothing and inert.
	defer func() { _ = os.RemoveAll(dir) }()
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(dir, "index")}

	read := invocation{args: []string{"read-tree", "--empty"}, env: env}
	if born {
		read.args = []string{"read-tree", string(parent)}
	}
	if _, err := r.exec.run(ctx, read); err != nil {
		return "", fmt.Errorf("git: read the current tree: %w", err)
	}

	var info bytes.Buffer
	for _, e := range edits {
		if e.remove {
			// Mode zero is how --index-info spells a removal. The all-zero id is
			// written at the repository's own object width so that a SHA-256
			// repository is not handed a SHA-1-shaped one.
			fmt.Fprintf(&info, "0 %s\t%s\x00", parent.zero(), e.path)
			continue
		}
		fmt.Fprintf(&info, "%s %s\t%s\x00", regularFileMode, e.blob, e.path)
	}
	if info.Len() > 0 {
		if _, err := r.exec.run(ctx, invocation{args: []string{"update-index", "-z", "--index-info"}, stdin: info.Bytes(), env: env}); err != nil {
			return "", fmt.Errorf("git: stage the change: %w", err)
		}
	}

	out, err := r.exec.run(ctx, invocation{args: []string{"write-tree"}, env: env})
	if err != nil {
		return "", fmt.Errorf("git: write the tree: %w", err)
	}
	tree := objectID(bytes.TrimSpace(out))
	if !tree.valid() {
		return "", fmt.Errorf("git: write-tree returned %q, which is not an object id", tree)
	}
	return tree, nil
}

// commitTree records tree as a commit on top of parent and returns it.
//
// The message travels on standard input and the identity in the environment, so
// neither can become an argument however it was spelled. commit-tree ignores
// commit.gpgsign and runs no hook, so what a write produces does not depend on
// the operator's configuration -- which is what makes two identical writes
// produce the same commit.
func (r *Repository) commitTree(ctx context.Context, tree, parent objectID, born bool, ch core.Change) (objectID, error) {
	args := []string{"commit-tree", string(tree)}
	if born {
		args = append(args, "-p", string(parent))
	}
	args = append(args, "-F", "-")

	out, err := r.exec.run(ctx, invocation{args: args, stdin: []byte(ch.Message), env: changeEnv(ch)})
	if err != nil {
		return "", fmt.Errorf("git: record the commit: %w", err)
	}
	commit := objectID(bytes.TrimSpace(out))
	if !commit.valid() {
		return "", fmt.Errorf("git: commit-tree returned %q, which is not an object id", commit)
	}
	return commit, nil
}

// publish moves the branch from old to next, and is the only step of a write
// that anything else can see.
//
// "git update-ref <ref> <new> <old>" is a genuine atomic compare-and-swap, and
// on an unborn branch the empty old value is its create-only form, so two
// writers racing to make the first commit behave exactly like two racing on any
// other update. A failure here is not classified: the caller re-reads and
// decides, because "the ref moved" and "this document changed" are different
// things.
func (r *Repository) publish(ctx context.Context, old, next objectID, born bool) error {
	previous := ""
	if born {
		previous = string(old)
	}
	_, err := r.exec.run(ctx, invocation{args: []string{"update-ref", operandSeparator, r.ref, string(next), previous}})
	return err
}

// changeEnv turns a [core.Change] into the environment commit-tree reads its
// identity from.
//
// The committer is the author. core gives a change one Author, and inventing a
// second identity -- the machine's, or Micawber's -- would put a name on the
// commit that nobody chose. A zero Change.Time is left unset so that git's own
// clock supplies it, which is the rule core states.
func changeEnv(ch core.Change) []string {
	env := []string{
		"GIT_AUTHOR_NAME=" + ch.Author.Name,
		"GIT_AUTHOR_EMAIL=" + ch.Author.Email,
		"GIT_COMMITTER_NAME=" + ch.Author.Name,
		"GIT_COMMITTER_EMAIL=" + ch.Author.Email,
	}
	if !ch.Time.IsZero() {
		stamp := ch.Time.Format(time.RFC3339)
		env = append(env, "GIT_AUTHOR_DATE="+stamp, "GIT_COMMITTER_DATE="+stamp)
	}
	return env
}
