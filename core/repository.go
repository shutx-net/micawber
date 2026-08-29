package core

import "context"

// ContentRepository stores Markdown documents. It is the boundary between the
// core and whatever actually holds the content: a Git working tree, a Git
// hosting API, or a plain directory. No implementation lives in this package.
//
// # Concurrency and revisions
//
// A headless CMS has concurrent editors and, with a Git backend, a store that
// can move underneath them, so every mutation is a compare-and-swap on an
// opaque [Revision].
//
// Put with a zero Content.Revision means create: it returns an error matching
// [ErrExists] if the path is already taken. Put with a non-empty
// Content.Revision means update: it returns [ErrConflict] if the stored
// revision differs and [ErrNotFound] if the document is gone. Put returns the
// revision of the newly stored content, which is always different from the one
// it replaced.
//
// Delete is the deliberate asymmetry: a zero revision deletes unconditionally,
// a non-empty one deletes only if it still matches. "Create a document that
// must not exist" is meaningful; "delete a document that must not exist" is
// not.
//
// # Errors
//
// Implementations return errors that satisfy errors.Is against the sentinels
// in this package: [ErrNotFound], [ErrExists], [ErrConflict] and [ErrInvalid].
// Wrapping with fmt.Errorf("...: %w", err) to add context is expected and does
// not break matching.
//
// # Obligations
//
// Implementations must honour ctx cancellation and return its error.
// They must not retain or mutate the Content passed to Put; the caller owns it
// and may reuse its Body and front-matter map afterwards. Values they return
// are the caller's to mutate freely, so a repository holding content in memory
// hands back a copy.
//
// Every mutating method takes a [Change] describing who is making it and why.
// A backend with no notion of authorship ignores it.
type ContentRepository interface {
	// List returns an entry for every Markdown document under c, at any depth,
	// sorted by path. Non-Markdown files are not listed.
	//
	// A collection is a prefix, not an object: one that holds nothing yields an
	// empty slice and a nil error, never ErrNotFound. Listing returns entries
	// rather than documents so that drawing an index stays proportional to the
	// tree instead of reading and decoding every file.
	List(ctx context.Context, c Collection) ([]ContentEntry, error)

	// Get returns the document at p, or an error matching ErrNotFound if there
	// is none. The returned Content carries the revision it was read at, which
	// the caller passes back to Put or Delete to make the write conditional.
	Get(ctx context.Context, p ContentPath) (Content, error)

	// Put stores c and returns its new revision. A zero c.Revision creates and
	// a non-empty one updates; see the type's documentation for the full
	// compare-and-swap contract. An invalid Content or Change is rejected with
	// an error matching ErrInvalid.
	Put(ctx context.Context, c Content, ch Change) (Revision, error)

	// Delete removes the document at p. A zero rev deletes unconditionally; a
	// non-empty one deletes only while it matches the stored revision, and
	// returns an error matching ErrConflict otherwise.
	//
	// Deleting something that is not there is an error matching ErrNotFound,
	// not a silent success: the repository reports what it observed, and a
	// caller that wants idempotence can test for it. A rename is a Get, a Put
	// and a Delete at the caller until a batched write exists.
	Delete(ctx context.Context, p ContentPath, rev Revision, ch Change) error
}
