// Package localfs implements [core.AssetStore] against a local filesystem
// directory.
//
// It links no Go module. Its whole non-standard-library surface is
// [github.com/shutx-net/micawber/core], and os, io/fs, path/filepath and
// syscall are confined to one file; TestLocalfsImportsAreAllowlisted and
// TestFilesystemAccessIsConfinedToTheRoot keep both true. There is no
// prerequisite to install: the substrate is the filesystem and the standard
// library is the filesystem API.
//
// The floor is Go 1.25, where os.Root gained its full method set including
// Rename. go.mod says 1.26, so the requirement is satisfied today; it is stated
// here so that a future downgrade fails with a reason rather than with a list
// of undefined methods.
//
// # The directory is the whole store
//
// There is no index, no sidecar, no manifest and no extended attribute. A file
// dropped into the directory by hand is an object on exactly the same terms as
// one written by [Store.Put], and [Store.Stat] reports the same thing about
// both: key, size and modification time from the entry, a content type derived
// from the extension, and no digest.
//
// That is the decision the package turns on, so the reasoning is worth stating.
// A filesystem has bytes and a modification time; it does not have a content
// type or a digest. Storing them somewhere means three cases at every read
// instead of one — metadata present and agreeing, metadata absent, metadata
// present and stale — and the derivation code still has to exist as the
// fallback for the second, so storing does not replace deriving, it adds a
// second sometimes-wrong source of truth on top of it. Stale is not
// hypothetical: a human opening logo.png in an editor and saving it changes the
// bytes without touching any sidecar, and [core.Digest] says the zero value
// means the store supplied none. An absent digest is honest; a wrong one is a
// correctness bug that reports success.
//
// The costs are real and are not argued away. The ContentType a caller passes
// to Put is not persisted and not reported: what comes back is derived from the
// key, so a Put of "application/x-custom" at "logo.png" reports "image/png". An
// extension the table does not know reports the empty string, which
// [core.AssetRef.Validate] permits. And a digest is available only from Put,
// where the bytes are already passing through: Stat is contractually forbidden
// to read the object, and hashing on demand would make an O(1) call O(size).
//
// # What Stat reports
//
// [Store.Stat] returns the key it was given, the size and modification time
// from the directory entry, a content type derived from the extension, and the
// zero digest. It reports nothing else, and in particular it never reads the
// object: core/store.go says Stat returns what the store knows "without reading
// it", which is what makes it an O(1) call an HTTP layer can afford before
// deciding whether to serve.
//
// For a file dropped into the directory by hand, "cp hero.jpg <dir>/photos/",
// Stat returns exactly what it returns for an object Put wrote: there is no
// notion of "objects the store wrote" as opposed to "files that are there", so
// there is no half-state, no repair path and no reconciliation. Four cases are
// not objects at all — a symbolic link of any target, a FIFO or socket or
// device node or directory, a name this store's key rules refuse such as
// "nul.png" or "logo.png ", and, for content type only, an extension the table
// does not know, which reports the empty string.
//
// # Content types
//
// The extension-to-media-type table this package ships is fixed, and
// mime.TypeByExtension is not used — an architecture test fails the build if it
// appears. Go's mime package augments its built-in table at init from
// /etc/mime.types and the XDG databases, so its answer is a property of the
// host: on a machine with those files present ".otf" resolves to an
// OpenDocument formula template. Portability is a product requirement, and a
// store whose content type depends on which machine it runs on is not portable.
// [WithMediaTypes] is how a deployment adds an extension the table lacks.
//
// # Keys
//
// A [core.AssetKey] becomes a filename directly, with no object database in
// between, so this store applies four rules on top of core's — and applies them
// on every platform, not only on Windows, so that the set of keys a store
// accepts does not vary with the host. A key is refused with
// [core.ErrInvalid] when any segment names a Windows device with or without an
// extension (CON, NUL, COM1, "nul.png"), ends with a dot or a space, is nothing
// but spaces, or contains any of < > : " | ? * .
//
// The hazard is silent data loss that reports success: on Windows a Put to
// "nul.png" writes to the null device, so the bytes vanish, the operation
// succeeds, and a later Get reports the object absent. An asset is referenced
// from a document committed to Git, so a key that resolves on one machine and
// not on another breaks the content that is meant to be the source of truth
// simply by being moved.
//
// Case is the filesystem's business. Keys are passed through byte for byte and
// nothing is normalized, so "Logo.PNG" and "logo.png" are two objects on Linux
// and one on a default macOS or Windows volume. Lower-casing keys would turn a
// quirk of two platforms into a rule for all four, and contradicts the
// principle [core.NewAssetKey] states in its own documentation: invalid input
// is rejected rather than normalized, and valid input is not rewritten.
// TestFilesystemCaseBehaviourIsRecorded states what the host running the suite
// actually does rather than encoding one platform's answer.
//
// # Concurrency
//
// A *Store is safe for concurrent use by multiple goroutines and holds no lock
// of its own. os.Root is documented safe for concurrent use, and the
// media-type table is written once at [Open] and read-only afterwards; there is
// nothing else. The guarantees are the filesystem's, which is the right place
// for them, because the filesystem is the only serialisation point every writer
// of the directory shares — another Micawber, a CI job, or the operator at a
// shell. A lock inside this process would make one process's writers orderly
// and do nothing about the others.
//
// Two Puts to one key: last writer wins, with no compare-and-swap, because
// [core.AssetStore] says assets have none and says why — the content repository
// is where concurrent editing happens. Both write their own temporary file and
// both rename; the later rename wins and the earlier object's inode is
// unlinked. No reader ever sees a mixture. Measured here at 480 Puts against
// 2,490 concurrent whole-object reads, zero torn and zero not-found; with the
// rename replaced by a write in place the same test reports thousands of torn
// reads, which is what says the assertion is real.
//
// A Put while a Get is streaming: the reader keeps the object it opened, because
// rename replaces the directory entry and not the inode. That is what makes it
// safe to serve a large asset over a slow connection while an editor replaces
// it. A Delete while a Get is streaming: the Delete succeeds, [Store.Stat]
// reports the key absent immediately, and the reader still completes — the
// inode survives until the last descriptor closes, which also means a forgotten
// Close pins the disk blocks as well as the descriptor.
//
// Concurrent Deletes of one key: exactly one caller is told it removed the
// object and the rest get [core.ErrNotFound]. Concurrent Puts to sibling keys
// under a prefix nobody has used yet need no coordination, because MkdirAll is
// idempotent under EEXIST.
//
// # The root, and what it does not defend against
//
// Every path operation goes through a single os.Root opened at the directory.
// The key is never joined onto the root by this package and filepath.EvalSymlinks
// is never called, which is what makes the check and the open fused per
// component: a joined-path validator is a time-of-check-to-time-of-use bug by
// construction, because the symlink can be planted between the check and the
// open. A symlink planted inside the directory therefore cannot turn a valid
// key into a read or a write outside it, and neither can a symlinked
// intermediate directory.
//
// The directory is a trust boundary rather than a sandbox, and os.Root's own
// documentation names three things it does not do. Two of them this store
// answers with a rule of its own; one it cannot answer at all.
//
// Hard links. A file outside the directory, hard-linked into it, is readable
// through the store, because a hard link is not a reference to a file, it is
// the file: there is no path-based defence and the only signal is a link count,
// which is not portable and which would also refuse the legitimate case of an
// operator de-duplicating two objects. The store does not try, and states the
// precondition instead — the directory is Micawber's, and anyone who can create
// entries in it can already write any bytes there. The exposure is one-way: a
// Put publishes by renaming a new file over the name, which replaces the
// directory entry and leaves the outside file's contents untouched, so a hard
// link is a disclosure risk on a read and never a corruption risk on a write.
//
// Filesystem boundaries and bind mounts. A separate filesystem or a bind mount
// inside the directory is traversable, and the store does not try to stop it:
// refusing to cross a device boundary would break the ordinary and sensible
// deployment of putting the asset directory on its own volume.
//
// Device files and FIFOs. This one is answered, because it is a live denial of
// service rather than a theoretical one: open(2) on a FIFO waits for a writer,
// so one mkfifo in the asset directory would hang a request thread
// indefinitely, and a context cannot interrupt a blocked open. See the
// regular-files-only rule below.
//
// # Regular files only
//
// [Store.Get], [Store.Stat] and [Store.Delete] check the entry before they act
// and report [core.ErrNotFound] for anything that is not a regular file: a
// directory, a symbolic link, a FIFO, a socket or a device node. A hard link to
// a regular file is indistinguishable from a regular file and is served.
//
// A symlink at a key is not followed and the answer does not depend on where it
// points — inside the directory, outside it, or nowhere is [core.ErrNotFound]
// in all three cases, and Readlink is never called. That is a security property
// rather than tidiness: under "follow links that stay inside" a caller would get
// three distinguishable answers and could map the operator's symlinks and infer
// what exists outside the directory. What it costs is that an operator cannot
// alias latest.png to 2026/08/logo.png with a symlink; a hard link or a copy
// does the same job and both are served.
//
// [Store.Put] is the exception, and safely so: it never opens the key. It
// writes a temporary file and renames over the name, so a symbolic link, a
// FIFO, a socket or a device node at the key is simply replaced by the object,
// the entry on the other side of a link is not written through, and a FIFO
// cannot block a write the way it would block an unguarded read. A directory
// is the one entry a rename will not replace, and that is [core.ErrExists] —
// so is a key whose ancestor is a regular file, since the filesystem cannot
// hold both "img/logo.png" and "img/logo.png/thumb.png" where an object store
// holds both happily.
//
// The check is an lstat before the open, and then a second check of the mode of
// the descriptor the open returned, so a regular file swapped for a directory
// in between is caught rather than failing mid-stream. One window stays open
// and is stated rather than papered over: an adversary who replaces a regular
// file with a FIFO between the two calls can still cause a blocked open,
// because the block happens before there is a descriptor to interrogate.
// Closing it needs O_NOFOLLOW|O_NONBLOCK, which is a per-platform constant —
// syscall.O_NONBLOCK does not exist on Windows — and therefore a second
// dangerous-surface file for the confinement guard to bless. The residual is a
// hang, never an escape, and it needs an adversary who can already write into
// the directory, which the trust precondition has already placed outside the
// boundary.
//
// # Removal
//
// [Store.Delete] removes regular files only, and removing something that is not
// there is [core.ErrNotFound], which is what [core.AssetStore] asks for and what
// makes Delete non-idempotent. A directory at the key is refused and left where
// it is, including an empty one: os.Root.Remove is unlinkat with a retry at
// AT_REMOVEDIR, so without the check it would take an empty directory happily.
// The residual race is benign and cannot be closed with the standard library —
// a directory created at exactly that path between the check and the removal is
// lost, and what is lost is an empty directory.
//
// Empty parent directories are never pruned. Walking up from img/2026/08 to
// remove them can take a directory a concurrent Put has just created and not
// yet renamed into, which would turn that writer's rename into ENOENT and fail
// a Put that should have succeeded. They are inert, they are visible, and the
// operator can remove them. They are also a genuine difference from an
// S3-compatible store, where a prefix exists exactly as long as an object under
// it does, and that difference is one of the things a shared contract test
// would have to be careful not to assume.
//
// # Writing
//
// [Store.Put] is atomic and durable. The bytes go to a temporary file in the
// key's own directory, created O_CREATE|O_EXCL under a name of 32 hex
// characters from crypto/rand; they are written, flushed to the platter,
// closed, and then renamed over the key, and the containing directory is
// flushed afterwards. rename(2) over an existing name is atomic, so a reader
// either opens the old object or the new one and never a mixture. The temporary
// file is in the same directory rather than in one shared at the root because
// rename must not cross a filesystem boundary and a subdirectory of the
// directory can be a separate mount.
//
// Durability is not optional and there is no knob to turn it off. Measured, a
// 4 KiB object costs 28 microseconds without the sync sequence and 5.0
// milliseconds with it. That is the right trade for this product for a reason
// specific to it: an asset is referenced from a document committed to Git, and
// if a power cut can lose the image while keeping the commit that points at it,
// the repository — the thing that is meant to be the source of truth — ends up
// describing a state that never existed. Assets are deliberately not in Git, so
// there is no revision history to recover them from. The directory flush is
// best-effort and its failure does not fail the Put: by the time it runs the
// object is already visible to every reader, so reporting a failure would be
// reporting one for a Put that succeeded.
//
// A failed or cancelled Put removes its temporary file and leaves the key
// alone, so a cancelled replacement leaves the previous object intact and
// readable. A killed process is the one case that leaves something behind: one
// temporary file, under a name no key can reach, and nothing else. The store
// does not sweep those at Open, because it cannot tell its own orphan from
// another process's Put in flight, and a sweep with an age heuristic is a race
// with a slow upload. It is an inert file, and removing it is a human's job or
// a cron's rather than the store's.
//
// The ref Put returns describes the bytes that Put wrote, taken from the
// temporary file before the rename rather than from the key afterwards. Under
// two racing Puts that means both refs are truthful and one of them describes
// an object no longer at the key, which is the only honest thing either can
// report where there is no compare-and-swap.
//
// Objects are created with mode 0644 and directories with 0755, both subject to
// the process umask. Because an object is published by renaming a new file over
// the old one, an operator's chmod on an object is reverted by the next Put.
// Writing in place would preserve the mode and is not an option: it is not
// atomic, and a reader would see a half-written object.
//
// # Reading
//
// [Store.Get] streams: it opens the object and hands back a reader over the
// descriptor, so serving a large asset costs one descriptor rather than its
// size in memory. The caller closes the reader, and a caller who forgets leaks
// one descriptor and, if the object has since been deleted, the disk blocks
// behind it. The store itself holds exactly one descriptor, the root, released
// by [Store.Close]; nothing is pooled, queued or reference-counted, so a
// forgotten Close cannot deadlock anything or block another operation.
//
// The reader is bound to the context [Store.Get] was given. Once that context
// is done, Read returns its error, which bounds a cancelled stream to one
// io.Copy buffer — 32 KiB — instead of to the length of the object. No
// goroutine watches the context: one per open object would leak for every
// caller who forgot Close, turning a bounded descriptor leak into an unbounded
// goroutine leak, and would make Close racy with an in-flight Read.
// Cancellation stops the stream; only Close frees the descriptor. A caller who
// wants the stream to outlive the call must pass a context that outlives it,
// which is the convention database/sql.Rows follows.
//
// # Errors
//
// Failures match errors.Is against [core.ErrNotFound], [core.ErrExists] and
// [core.ErrInvalid]. [core.ErrUnsupported] is not used: everything
// [core.AssetStore] asks for, a directory can do. A permission failure matches
// none of the sentinels — it is not a caller mistake, and classifying it as one
// would tell an HTTP layer to blame the request for something only the operator
// can fix.
//
// No error this package returns for a key operation carries a filesystem path:
// not the directory's own absolute path, not a joined path, and not a symlink
// target. The rule needs care rather than good intentions, because an error
// from an already-open file carries the joined absolute path in its message, so
// the store extracts the underlying condition and builds its own message around
// the key. It is the same rule the markdown package states about document bytes
// and the git package states about credentials. [Open] is the one exception and
// is deliberate: its error names the directory the caller passed it, which is
// the caller's own argument rather than anything discovered on the filesystem,
// and an Open failure that did not say which directory failed would be
// unusable.
//
// # Interoperability
//
// A directory this store has never seen is usable immediately: no migration, no
// index build, no first-run scan. A directory this store has written is an
// ordinary directory of ordinary files, which something that is not Micawber
// can read, replace and remove with no cooperation — and the store agrees with
// it on the next call, because there is no cached metadata to go stale. That is
// the same argument that makes Git the source of truth for content, applied to
// a directory.
//
// # What is deliberately not here
//
// There is no List. Building this backend showed that the hard part is not
// pagination but the folder view an admin UI would want first, and that the
// folder view does not agree between a filesystem and an object store — because
// this package refuses to prune empty directories, a delimiter-based listing
// here would report a prefix with nothing under it, which S3 structurally
// cannot do. The signature both backends can satisfy is known,
// List(ctx, prefix, after, limit) returning a page and a cursor that is simply
// the last key returned; it is not added because there is no caller yet, and
// because it would be a change to [core.AssetStore] that every future store
// would have to implement. The trigger is a real consumer or a second backend.
//
// There is no URL, signed or public, and [core.AssetRef] carries none. A local
// directory has no host, no scheme and no signing key: the bytes become
// reachable only when an HTTP handler, a reverse proxy or a CDN origin is
// pointed at the directory. When a URL does exist it belongs to an optional
// interface that delivery-capable stores implement and callers discover by type
// assertion, outside [core.AssetStore]; a signed URL is a capability with an
// audience and an expiry, and an AssetRef is a fact about an object that does
// not expire.
//
// There is no maximum object size, no quota and no disk-space check. Those are
// policy numbers and configuration owns them; putting one here would make it
// unconfigurable. The consequence is worth stating: a Put with
// [core.SizeUnknown] reads to EOF with no bound, so the store is a disk-fill
// vector for whoever can reach it, and today nothing network-facing can.
package localfs
