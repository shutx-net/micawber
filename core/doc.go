// Package core is the provider-free core of Micawber, a portable, Git-native
// headless CMS.
//
// It defines the domain model — validated content paths, collections, asset
// keys, documents and assets — together with the small interfaces every
// storage adapter implements. It imports only the standard library, and
// nothing in it knows what Git, GitHub, GitLab, S3 or a filesystem are.
// Provider code lives in sibling packages that depend on this one; the
// dependency never runs the other way, and TestCoreImportsOnlyStandardLibrary
// keeps that true.
//
// Git is the source of truth for content and history, so this package
// deliberately does not reimplement persistence, revisions or rollback.
//
// # Values
//
// [ContentPath], [Collection] and [AssetKey] can only be built through their
// constructors, which reject rather than normalize: a path that traverses out
// of the content root is an error to be surfaced, not something to quietly
// clean up. Every adapter therefore receives addressing it can trust, and
// path traversal cannot be reintroduced by a forgotten check.
//
// A document is a [Content]: its path, its [FrontMatter] and the verbatim body
// bytes that follow the front-matter block. Front matter keeps both its raw
// bytes and its decoded fields so that a serializer can re-emit the raw block
// unchanged while the fields still match it, which keeps Git diffs limited to
// what the author actually edited. This package neither parses nor serializes
// Markdown.
//
// # Storage
//
// [ContentRepository] stores documents and [AssetStore] stores binary assets.
// They are separate because assets do not belong in Git history.
// [ContentHistory] is an optional capability that a repository backed by a
// version control system may also implement; callers detect it with a type
// assertion.
//
// # Operation contract
//
// These rules are the same for every implementation, and adapter tests should
// hold them to it.
//
// A [Revision] is opaque. Only the repository that issued one may interpret
// it: a Git backend might use an object id, a filesystem store a size and
// modification time, an object store an ETag. Callers compare revisions and
// pass them back; they never parse them.
//
// ContentRepository.Put is a compare-and-swap.
//
//   - A zero Content.Revision means create. If the path is already taken, Put
//     returns an error matching [ErrExists].
//   - A non-empty Content.Revision means update. If the stored revision
//     differs, Put returns [ErrConflict]; if the document is gone, [ErrNotFound].
//   - Put returns the revision of the newly stored content. It differs from the
//     revision it replaced whenever the stored bytes differ; a Put whose bytes
//     are identical to what is stored changes nothing and returns the same
//     revision.
//
// ContentRepository.Delete is deliberately asymmetric with Put. A zero
// revision deletes unconditionally; a non-empty one deletes only while it
// still matches, and returns [ErrConflict] otherwise. "Create a document that
// must not exist" is meaningful; "delete a document that must not exist" is
// not. Deleting something absent is [ErrNotFound], for content and assets
// alike: the store reports what it observed, and a caller that wants
// idempotence tests for it.
//
// ContentRepository.List is recursive under the given [Collection], yields
// only Markdown paths, and is sorted by path. It returns [ContentEntry]
// values, not whole documents, so that drawing an index stays proportional to
// the tree instead of reading and decoding every file. A collection is a
// prefix rather than an object, so one that holds nothing is an empty slice
// and a nil error, never [ErrNotFound].
//
// Every mutation carries a [Change]: a message, an [Author] and a time. Each
// one becomes a commit on a Git backend, and the message and author come from
// the user rather than from provider configuration, which is why they travel
// with the call instead of hiding in configuration or in a context. A backend
// with no notion of authorship ignores the Change. A zero Change.Time means
// the backend supplies its own clock.
//
// Implementations honour context cancellation, do not retain or mutate the
// values passed to them, return values the caller may freely mutate, and
// return errors that satisfy errors.Is against the sentinels in this package.
//
// # Errors
//
// [ErrNotFound], [ErrExists], [ErrConflict], [ErrInvalid] and [ErrUnsupported]
// are the vocabulary an HTTP layer maps to status codes. They survive wrapping
// with fmt.Errorf and the %w verb, which adapters are expected to do.
// [ValidationError] adds which value was rejected and why, and unwraps to
// [ErrInvalid].
package core
