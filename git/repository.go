package git

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/shutx-net/micawber/core"
	"github.com/shutx-net/micawber/markdown"
)

// The capabilities this adapter provides, asserted where the compiler can see
// them. TestRepositorySatisfiesTheCoreInterfaces says the same thing in a form a
// reader can grep for.
var (
	_ core.ContentRepository = (*Repository)(nil)
	_ core.ContentHistory    = (*Repository)(nil)
)

// defaultGitBinary is looked up on PATH when WithGitBinary says nothing.
const defaultGitBinary = "git"

// branchRefPrefix is where a bare branch name lives once qualified.
const branchRefPrefix = "refs/heads/"

// Repository is a [core.ContentRepository] and [core.ContentHistory] backed by a
// local Git repository.
//
// It holds a git binary, a directory, a branch ref and a content root, all fixed
// at [Open] and never written again, so a *Repository is safe for concurrent use.
// Every operation resolves the branch afresh: there is no cached commit to go
// stale, which is why a commit made by plain git is visible to the very next
// call.
//
// Correctness under concurrency comes from the compare-and-swap on the branch
// ref, and from nothing else. That is the right place for it because it is the
// only serialisation point every writer of the repository shares -- other
// processes, other machines and the user's own git included -- and no lock inside
// this process could stand in for it.
//
// Reads run freely and in parallel. Writes from this one process take turns, in
// a queue that is an efficiency measure rather than a safety one: a branch ref
// admits one write at a time whatever anybody does, so letting this process's own
// writers stampede it costs each of them a rebuilt tree and a wasted commit and
// buys nothing. Measured at eight concurrent writers on distinct documents, the
// stampede lost 73 of 160 writes to exhausted retries; taking turns loses none
// and finishes sooner. Writers in other processes are still handled the only way
// they can be, by the compare-and-swap and the retry above it.
type Repository struct {
	// exec runs git against the repository directory.
	exec runner
	// ref is the fully qualified branch the adapter reads and writes.
	ref string
	// root is the directory within the repository that content lives under.
	root core.Collection
	// writes admits one publish from this process at a time. It is created at
	// Open and never replaced, so the value stays immutable; what it holds is a
	// queue, not state.
	writes chan struct{}
}

// options is the configuration Open assembles before anything runs.
type options struct {
	bin  string
	ref  string
	root core.Collection
}

// Option configures [Open]. Options are applied and validated before any
// process starts, so a misconfiguration is reported without touching the
// repository.
type Option func(*options) error

// WithGitBinary names the git executable to drive, resolved on PATH when it is
// not a path. It defaults to "git".
//
// It exists because the git a deployed Micawber drives is an operational choice:
// a pinned build, a wrapper, or a binary somewhere PATH does not reach.
func WithGitBinary(path string) Option {
	return func(o *options) error {
		if path == "" {
			return sentinelf(core.ErrInvalid, nil, "WithGitBinary: the path is empty")
		}
		o.bin = path
		return nil
	}
}

// WithBranch names the branch the adapter reads and writes, as either a bare
// name ("main") or a full ref ("refs/heads/main"). It defaults to the branch
// HEAD points at.
//
// A caller who has deliberately detached HEAD, or who wants to write somewhere
// other than the checked-out branch, says so here.
func WithBranch(ref string) Option {
	return func(o *options) error {
		if strings.TrimSpace(ref) == "" {
			return sentinelf(core.ErrInvalid, nil, "WithBranch: the branch is empty")
		}
		if err := safeArg(ref); err != nil {
			return err
		}
		if strings.ContainsAny(ref, " \t\n\r") {
			return sentinelf(core.ErrInvalid, nil, "WithBranch: %q contains whitespace", ref)
		}
		if !strings.HasPrefix(ref, "refs/") {
			ref = branchRefPrefix + ref
		}
		o.ref = ref
		return nil
	}
}

// WithContentRoot bounds the adapter to a directory inside the repository, so
// that a repository holding a site's own files beside its content can be
// addressed without exposing them. It defaults to the repository root.
//
// Every path the caller gives is joined onto the root with [core.Collection.Join],
// which validates before it joins and therefore cannot produce a path outside
// it, and every path git reports is mapped back with Rel. Escaping the root is
// impossible by construction rather than by a check this package could forget.
func WithContentRoot(root core.Collection) Option {
	return func(o *options) error {
		// core.NewCollection accepts a leading dash, and is right to: it is a
		// legal directory name and not a traversal. It is hazardous only to a
		// route that builds argument vectors, so it is refused here, at Open,
		// rather than in core where it would become every adapter's rule.
		if !root.IsRoot() {
			if err := safeArg(root.String()); err != nil {
				return err
			}
		}
		o.root = root
		return nil
	}
}

// Open opens the Git repository at dir.
//
// dir may be a working tree, a directory inside one, or a bare repository; every
// path the adapter builds is repository-relative, so where inside it the command
// runs makes no difference. The repository must already exist: running git init
// is the operator's decision, not Micawber's.
//
// Open resolves the branch to write to and fails with an error matching
// [core.ErrInvalid] when there is none -- a detached HEAD with no [WithBranch],
// a directory that is not a repository, or a git binary that cannot be found. It
// does not require the branch to have any commits: an unborn branch opens
// cleanly, lists empty, and takes its first commit from the first Put.
func Open(ctx context.Context, dir string, opts ...Option) (*Repository, error) {
	o := options{bin: defaultGitBinary}
	for _, opt := range opts {
		if opt == nil {
			return nil, sentinelf(core.ErrInvalid, nil, "open %q: a nil Option was given", dir)
		}
		if err := opt(&o); err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(dir) == "" {
		return nil, sentinelf(core.ErrInvalid, nil, "open: the repository directory is empty")
	}

	bin, err := lookGit(o.bin)
	if err != nil {
		return nil, err
	}

	r := &Repository{
		exec:   runner{bin: bin, dir: dir},
		ref:    o.ref,
		root:   o.root,
		writes: make(chan struct{}, 1),
	}
	if _, err := r.exec.run(ctx, invocation{args: []string{"rev-parse", "--git-dir"}}); err != nil {
		return nil, sentinelf(core.ErrInvalid, err, "open %q: not a Git repository", dir)
	}
	if r.ref == "" {
		out, err := r.exec.run(ctx, invocation{args: []string{"symbolic-ref", "HEAD"}})
		if err != nil {
			return nil, sentinelf(core.ErrInvalid, err, "open %q: HEAD is detached, so there is no branch to compare-and-swap on; name one with WithBranch", dir)
		}
		r.ref = strings.TrimSpace(string(out))
		if err := safeArg(r.ref); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// List returns an entry for every Markdown document under c, at any depth,
// sorted by path.
//
// It is one tree walk: "git ls-tree -r" already carries a blob's object id on
// every record, which is exactly the revision, so a listing costs one process
// however many documents it holds and never reads or decodes a document. A
// collection is a prefix rather than an object, so one that holds nothing -- or a
// branch with no commits at all -- is an empty slice and a nil error.
//
// Entries a caller could not use are skipped rather than reported or fatal: a
// path core cannot represent, a submodule, a symbolic link, and anything that is
// not a Markdown file. A damaged document is still listed, because List does not
// decode, and that is how the document that Get refuses stays findable.
func (r *Repository) List(ctx context.Context, c core.Collection) ([]core.ContentEntry, error) {
	prefix, err := r.listPrefix(c)
	if err != nil {
		return nil, err
	}

	args := []string{"ls-tree", "-r", "-z", "--full-tree", r.ref}
	if prefix != "" {
		spec, err := pathspec(prefix)
		if err != nil {
			return nil, err
		}
		args = append(args, "--", spec)
	}

	out, err := r.exec.run(ctx, invocation{args: args})
	if err != nil {
		// An unborn branch is the one failure that is not one. Asking git rather
		// than reading its message costs a process only on the path that was
		// going to return nothing anyway.
		if _, born, headErr := r.head(ctx); headErr == nil && !born {
			return []core.ContentEntry{}, nil
		}
		return nil, fmt.Errorf("git: list %q: %w", c, err)
	}

	records, err := parseLsTree(out)
	if err != nil {
		return nil, fmt.Errorf("git: list %q: %w", c, err)
	}

	entries := make([]core.ContentEntry, 0, len(records))
	for _, rec := range records {
		if !rec.isDocument() {
			continue
		}
		p, ok := r.contentPath(rec.path)
		if !ok || !p.IsMarkdown() {
			continue
		}
		entries = append(entries, core.ContentEntry{Path: p, Revision: core.Revision(rec.oid)})
	}

	// ls-tree already returns entries in tree order, which for a recursive
	// listing is byte order on the whole path. Sorting anyway is cheap, and
	// relying on an undocumented ordering to satisfy a documented one is how a
	// contract breaks quietly.
	slices.SortFunc(entries, func(a, b core.ContentEntry) int {
		return strings.Compare(a.Path.String(), b.Path.String())
	})
	return entries, nil
}

// listPrefix returns the repository-relative directory a listing is bounded to:
// the collection joined onto the content root, or the empty string when both are
// the root and the whole tree is in scope.
func (r *Repository) listPrefix(c core.Collection) (string, error) {
	switch {
	case c.IsRoot() && r.root.IsRoot():
		return "", nil
	case c.IsRoot():
		return r.root.String(), nil
	case r.root.IsRoot():
		return c.String(), nil
	}
	joined, err := r.root.Join(c.String())
	if err != nil {
		return "", sentinelf(core.ErrInvalid, err, "collection %q is not within the content root %q", c, r.root)
	}
	return joined.String(), nil
}

// Get returns the document at p, read from the branch's current tip.
//
// The revision it carries is the blob's object id, which the caller passes back
// to Put or Delete to make the write conditional on nothing else having changed
// that document.
//
// A path that is absent, or that names a directory or a submodule, is an error
// matching [core.ErrNotFound]. A file that is not a well-formed Markdown document
// is [core.ErrInvalid], passed through from the parser, because there is no
// honest [core.Content] to return for it; the document stays listable and
// deletable so that it can be repaired.
//
// Get is one "git cat-file --batch", which resolves the path and streams the
// object in a single spawn. The cost of that is that it sees an object and not a
// tree entry, so it cannot tell a regular file from a symbolic link and returns
// the link's target text as though it were a document. List does know the
// difference and leaves links out, so a path that came from a listing can never
// be one; the asymmetry is worth a second process only if links in content
// repositories ever stop being a curiosity.
func (r *Repository) Get(ctx context.Context, p core.ContentPath) (core.Content, error) {
	repoRelative, err := r.repoPath(p)
	if err != nil {
		return core.Content{}, err
	}

	oid, data, err := r.readBlob(ctx, r.ref+":"+repoRelative, fmt.Sprintf("no document at %q", p))
	if err != nil {
		return core.Content{}, err
	}

	doc, err := markdown.Parse(data)
	if err != nil {
		// markdown never embeds a byte of the document in its errors, and this
		// wrapper must not undo that: the path is addressing, which core already
		// documents as safe to log.
		return core.Content{}, fmt.Errorf("git: document %q: %w", p, err)
	}

	// The Layout is deliberately dropped here. It is a serialization artefact
	// with no domain meaning, so it does not belong in core.Content, and Put
	// recovers it from the stored object it has to read anyway.
	c := doc.Content
	c.Path = p
	c.Revision = core.Revision(oid)
	return c, nil
}

// Put stores c and returns the revision of the newly stored content.
//
// A zero c.Revision creates: the path must be free, and a path that is taken is
// an error matching [core.ErrExists]. A create has no previous shape, so it is
// written with markdown's canonical delimiters.
//
// A non-empty c.Revision updates, and only while it still matches the blob
// stored at that path: a different one is [core.ErrConflict] and an absent one is
// [core.ErrNotFound]. Because the revision is the document's own blob id and not
// the repository's, someone else committing a different document is not a
// conflict -- the write is rebuilt on their commit and published again.
//
// An update re-emits into the shape the stored document was written in, which the
// same read that checked the revision recovers, so delimiters, line endings and a
// byte-order mark survive an edit that only touched the content. A Put whose
// bytes come out identical to what is stored writes nothing at all and returns the
// revision the caller already held.
//
// The Change becomes the commit: its message, its author, and its time when it
// has one -- a zero Change.Time leaves the timestamp to git's own clock, which is
// what core specifies. The committer is the author, because core gives a change
// one identity and inventing a second would put a name on the commit that nobody
// chose.
func (r *Repository) Put(ctx context.Context, c core.Content, ch core.Change) (core.Revision, error) {
	// core's rules, in core. The adapter adds context and does not restate them.
	if err := c.Validate(); err != nil {
		return "", fmt.Errorf("git: put: %w", err)
	}
	if err := ch.Validate(); err != nil {
		return "", fmt.Errorf("git: put %q: %w", c.Path, err)
	}
	repoRelative, err := r.repoPath(c.Path)
	if err != nil {
		return "", err
	}

	return r.apply(ctx, ch, func(ctx context.Context, parent objectID, born bool) ([]edit, core.Revision, bool, error) {
		if c.Revision.IsZero() {
			return r.planCreate(ctx, c, repoRelative, parent, born)
		}
		return r.planUpdate(ctx, c, repoRelative, parent, born)
	})
}

// planCreate is the write a zero revision asks for: the path must be free.
func (r *Repository) planCreate(ctx context.Context, c core.Content, repoRelative string, parent objectID, born bool) ([]edit, core.Revision, bool, error) {
	if born {
		entries, err := r.catFile(ctx, string(parent)+":"+repoRelative)
		if err != nil {
			return nil, "", false, err
		}
		// Anything at all -- a document, a directory, a symbolic link -- is
		// something a create must not write over.
		if !entries[0].missing {
			return nil, "", false, sentinelf(core.ErrExists, nil, "a document already exists at %q", c.Path)
		}
	}

	data, err := markdown.Format(c)
	if err != nil {
		return nil, "", false, fmt.Errorf("git: serialize %q: %w", c.Path, err)
	}
	blob, err := r.hashObject(ctx, data)
	if err != nil {
		return nil, "", false, err
	}
	return []edit{{path: repoRelative, blob: blob}}, core.Revision(blob), true, nil
}

// writePlan decides what one attempt of a write does against a given parent
// commit: which edits to make, which revision to report, and whether there is
// anything to publish at all.
//
// It is a function rather than a flag because Put and Delete disagree about what
// absence means -- creating something that is not there is the point, deleting
// something that is not there is an error -- and core spends a paragraph
// justifying that asymmetry. A shared predicate with a boolean is where such a
// justification goes to be forgotten.
type writePlan func(ctx context.Context, parent objectID, born bool) ([]edit, core.Revision, bool, error)

// maxPublishAttempts bounds the publish retry, and publishBackoff spreads the
// attempts out.
//
// The loop exists because a failed compare-and-swap usually means somebody
// committed something, not that this document changed, so giving up on the first
// failure would report a conflict that did not happen. It is bounded because an
// unbounded retry under sustained write load is a hang wearing a correctness
// costume: exhausting it says so, in an error matching none of core's conditions,
// because none of them describes it.
//
// The backoff is for the writers this process cannot queue: another Micawber, or
// the user's own git. A write holds no lock while it builds its tree and its
// commit, so with n such writers in flight the branch moves about n times per
// attempt and an immediate retry just loses again, in lockstep. Measured at eight
// concurrent writers with neither the queue nor a backoff, five attempts lost 73
// of 160 writes; a jittered backoff alone brought that to 26. The jitter is what
// breaks the lockstep, and the ceiling keeps a late attempt from waiting longer
// than a caller would.
const (
	maxPublishAttempts = 5
	publishBackoff     = 2 * time.Millisecond
	maxPublishBackoff  = 64 * time.Millisecond
)

// waitBeforeRetry sleeps for a random interval below an exponentially growing
// ceiling, and reports whether ctx survived it.
//
// Full jitter rather than a fixed delay: two writers that back off by the same
// amount collide again on exactly the same schedule, which is the failure the
// backoff exists to prevent.
func waitBeforeRetry(ctx context.Context, attempt int) error {
	ceiling := min(publishBackoff<<attempt, maxPublishBackoff)
	timer := time.NewTimer(rand.N(ceiling) + time.Millisecond)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// apply resolves the branch, asks plan what to do, and publishes the result,
// retrying while the branch keeps moving underneath it.
//
// It is the only place in the package that moves a ref, so the queue, the
// compare-and-swap, the commit and the retry live in one function that Put and
// Delete share. Every attempt starts from a freshly resolved branch: rebuilding
// on a stale parent is how a retry loses somebody else's commit.
//
// A failed publish is deliberately not classified here. "the branch moved" and
// "this document changed" are different facts, and only plan can tell them apart,
// so the loop re-asks rather than reading git's message.
func (r *Repository) apply(ctx context.Context, ch core.Change, plan writePlan) (core.Revision, error) {
	// Take a turn. Acquiring through a select rather than a mutex means a caller
	// whose context is already done, or who gives up while waiting, is not stuck
	// behind a write that has wedged.
	select {
	case r.writes <- struct{}{}:
		defer func() { <-r.writes }()
	case <-ctx.Done():
		return "", ctx.Err()
	}

	var lastErr error

	for attempt := 1; attempt <= maxPublishAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}

		parent, born, err := r.head(ctx)
		if err != nil {
			return "", err
		}
		edits, rev, wanted, err := plan(ctx, parent, born)
		if err != nil {
			return "", err
		}
		if !wanted {
			return rev, nil
		}

		tree, err := r.writeTree(ctx, parent, born, edits)
		if err != nil {
			return "", err
		}
		commit, err := r.commitTree(ctx, tree, parent, born, ch)
		if err != nil {
			return "", err
		}
		if err := r.publish(ctx, parent, commit, born); err != nil {
			if ctx.Err() != nil {
				return "", err
			}
			lastErr = err
			if err := waitBeforeRetry(ctx, attempt); err != nil {
				return "", err
			}
			continue
		}
		return rev, nil
	}

	return "", fmt.Errorf("git: the branch %s moved under all %d attempts to publish: %w", r.ref, maxPublishAttempts, lastErr)
}

// planUpdate is the write a non-empty revision asks for: the document must still
// be the one the caller read.
func (r *Repository) planUpdate(ctx context.Context, c core.Content, repoRelative string, parent objectID, born bool) ([]edit, core.Revision, bool, error) {
	if !born {
		return nil, "", false, sentinelf(core.ErrNotFound, nil, "no document at %q", c.Path)
	}

	// One read, used twice: the object id decides the compare-and-swap and the
	// bytes carry the shape to write back into. That is what makes preserving
	// the layout cost nothing.
	entries, err := r.catFile(ctx, string(parent)+":"+repoRelative)
	if err != nil {
		return nil, "", false, err
	}
	stored := entries[0]
	if stored.missing || stored.typ != blobType {
		return nil, "", false, sentinelf(core.ErrNotFound, nil, "no document at %q", c.Path)
	}
	if core.Revision(stored.oid) != c.Revision {
		return nil, "", false, sentinelf(core.ErrConflict, nil, "the document at %q has changed since it was read", c.Path)
	}

	data, err := r.serializeInto(stored.data, c)
	if err != nil {
		return nil, "", false, err
	}
	// Byte equality is the only test applied, and it is the right one because it
	// is the test Git itself applies. Nothing here inspects a field or decides
	// that a change is too small to record.
	if bytes.Equal(data, stored.data) {
		return nil, c.Revision, false, nil
	}

	blob, err := r.hashObject(ctx, data)
	if err != nil {
		return nil, "", false, err
	}
	return []edit{{path: repoRelative, blob: blob}}, core.Revision(blob), true, nil
}

// serializeInto writes c into the shape the stored bytes were written in.
//
// This is where the byte-fidelity guarantee crosses the repository boundary. The
// caller never sees a markdown.Layout -- it is a serialization artefact with no
// domain meaning, so it has no business in core.ContentRepository -- and it does
// not have to, because the stored object is re-parsed for it.
//
// Bytes that will not parse have no shape to preserve, so they fall back to the
// canonical one. That is the one case the guarantee does not cover, and it covers
// a file that had no coherent shape in the first place.
func (r *Repository) serializeInto(stored []byte, c core.Content) ([]byte, error) {
	previous, err := markdown.Parse(stored)
	if err != nil {
		data, formatErr := markdown.Format(c)
		if formatErr != nil {
			return nil, fmt.Errorf("git: serialize %q: %w", c.Path, formatErr)
		}
		return data, nil
	}

	data, err := markdown.Document{Content: c, Layout: previous.Layout}.Bytes()
	if err != nil {
		return nil, fmt.Errorf("git: serialize %q: %w", c.Path, err)
	}
	return data, nil
}

// Delete removes the document at p and records the removal as a commit.
//
// A zero rev deletes unconditionally; a non-empty one deletes only while it still
// matches the stored blob, and is [core.ErrConflict] otherwise. Deleting
// something that is not there is [core.ErrNotFound] either way -- unconditional
// means "whatever revision it has", not "even if it is absent" -- so a caller
// that wants idempotence tests for it.
//
// Delete never parses the document. It needs the blob id for the compare-and-swap
// and nothing else, which is what makes it the way to remove a file that Get
// refuses to return: a bad merge is repaired with Put or Delete, not by hand.
//
// The parent directory disappears with the last document in it, because Git has
// no empty trees. That costs nothing here and is worth knowing, since it is
// behaviour a filesystem-backed adapter would have to implement deliberately.
func (r *Repository) Delete(ctx context.Context, p core.ContentPath, rev core.Revision, ch core.Change) error {
	if err := ch.Validate(); err != nil {
		return fmt.Errorf("git: delete %q: %w", p, err)
	}
	repoRelative, err := r.repoPath(p)
	if err != nil {
		return err
	}

	_, err = r.apply(ctx, ch, func(ctx context.Context, parent objectID, born bool) ([]edit, core.Revision, bool, error) {
		return r.planDelete(ctx, p, rev, repoRelative, parent, born)
	})
	return err
}

// planDelete is Delete's half of the asymmetry core spends a paragraph on:
// absence is always an error, and the revision is checked only when there is one.
func (r *Repository) planDelete(ctx context.Context, p core.ContentPath, rev core.Revision, repoRelative string, parent objectID, born bool) ([]edit, core.Revision, bool, error) {
	if !born {
		return nil, "", false, sentinelf(core.ErrNotFound, nil, "no document at %q", p)
	}

	entries, err := r.catFile(ctx, string(parent)+":"+repoRelative)
	if err != nil {
		return nil, "", false, err
	}
	stored := entries[0]
	if stored.missing || stored.typ != blobType {
		return nil, "", false, sentinelf(core.ErrNotFound, nil, "no document at %q", p)
	}
	if !rev.IsZero() && core.Revision(stored.oid) != rev {
		return nil, "", false, sentinelf(core.ErrConflict, nil, "the document at %q has changed since it was read", p)
	}
	return []edit{{path: repoRelative, remove: true}}, "", true, nil
}

// repoPath returns p as git sees it: the content root joined onto it.
//
// Join validates the relative part before it joins, so the result can never
// leave the root however p was built -- a ContentPath cannot hold ".." in the
// first place, and this is where that guarantee is spent.
func (r *Repository) repoPath(p core.ContentPath) (string, error) {
	if p.IsZero() {
		return "", sentinelf(core.ErrInvalid, nil, "the content path is empty")
	}
	joined, err := r.root.Join(p.String())
	if err != nil {
		return "", sentinelf(core.ErrInvalid, err, "content path %q is not within the content root %q", p, r.root)
	}
	return joined.String(), nil
}

// contentPath is repoPath backwards: it turns a repository-relative path git
// reported into one relative to the content root, and reports whether the path
// is one core can represent and the root contains at all.
func (r *Repository) contentPath(repoRelative string) (core.ContentPath, bool) {
	full, err := core.NewContentPath(repoRelative)
	if err != nil {
		return core.ContentPath{}, false
	}
	rel, ok := r.root.Rel(full)
	if !ok {
		return core.ContentPath{}, false
	}
	p, err := core.NewContentPath(rel)
	if err != nil {
		return core.ContentPath{}, false
	}
	return p, true
}

// pathspec wraps a repository-relative path as a Git pathspec meaning exactly
// that path and nothing else.
//
// Both magics are load-bearing. "literal" turns off the wildcard matching a
// pathspec has by default, without which "git log -- posts/a*.md" reports the
// history of every document whose name it happens to match -- measured -- and a
// Revision from one document could satisfy GetRevision's membership check for
// another. "top" makes the pathspec relative to the repository root rather than
// to whichever directory inside it Open was given.
//
// The leading-dash check is the first line rather than the second. The wrapper
// already makes a dash harmless, since the operand now begins with a colon, but
// the rule this adapter keeps is that no caller-supplied value becomes a git
// argument that could be read as an option -- so it is checked before it is
// wrapped, and stays true however the wrapping later changes.
func pathspec(repoRelative string) (string, error) {
	if err := safeArg(repoRelative); err != nil {
		return "", err
	}
	return ":(top,literal)" + repoRelative, nil
}
