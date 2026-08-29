// Package git implements [core.ContentRepository] and [core.ContentHistory]
// against a local Git repository, by driving the user's git binary with plumbing
// commands.
//
// It links no Git library. Its whole non-standard-library surface is
// [github.com/shutx-net/micawber/core] and
// [github.com/shutx-net/micawber/markdown], and os/exec is confined to one file;
// TestGitImportsAreAllowlisted and TestOSExecIsConfinedToTheRunner keep both true.
// The cost of that is a prerequisite, stated plainly rather than hidden: git must
// be installed. It is looked up on PATH, or named with [WithGitBinary], and the
// floor is git 2.2 (2014), set by the strict ISO 8601 "%aI" the history format
// uses; every other command this package runs is older.
//
// # Why the binary
//
// The alternative was a pure-Go library, and it was measured rather than argued
// about. Three things decided it. Interoperability: go-git takes an advisory
// flock(2) on a ref file where git creates a "<ref>.lock" sidecar and renames it
// into place, so with a lock held git refuses and go-git proceeds -- two writers
// that cannot see each other, in exactly the shared repository this product
// exists to be native to. Portability: go-git cannot open a SHA-256 repository at
// all, which git has created since 2.29. Weight: 21 modules and +5.08 MiB of
// binary against this package's zero and +0.36 MiB. Driving git also means
// Micawber never holds a credential: the user's credential helper, ssh-agent and
// ssh configuration do that work, which is why the runner starts from the
// process environment rather than a scrubbed one.
//
// What it costs is process spawns, measured at 1.25 ms each: a Get is one
// process, a List is one, a Put about six. For an admin CMS whose write rate is
// human-paced that is the right trade, and it is the operations an index hammers
// -- List over 2000 documents in 3.3 ms, path-filtered history in 5.4 ms -- that
// the subprocess wins outright, because one git does the whole walk in C.
//
// # The object layer, and nothing else
//
// The adapter reads and writes Git objects and exactly one branch ref. It never
// reads the working tree, never opens the user's index -- writes are staged
// through a scratch index named by GIT_INDEX_FILE, in a temporary directory
// removed afterwards -- and never runs porcelain. Three things follow.
//
// Byte fidelity survives. "git hash-object -w --stdin" applies no clean filter
// when no --path is given and "git cat-file blob" applies no smudge filter, so
// what markdown serialized is what Git stores and what Get reads back, whatever
// the repository's core.autocrlf or .gitattributes say. The byte-level guarantee
// the markdown package makes is therefore still a guarantee here, rather than
// becoming a property of the user's configuration.
//
// Writes are predictable. "git commit-tree" ignores commit.gpgsign, and neither
// commit-tree nor update-ref runs the pre-commit or commit-msg hooks. A
// repository's reference-transaction hook does still fire on update-ref, which is
// Git's behaviour and not something this package should suppress.
//
// A dirty working tree cannot affect correctness, because nothing here reads one.
// The honest converse is that a checkout sharing the repository will report
// staged modifications after a write -- the branch moved under an index that has
// not been refreshed -- so Micawber should own its repository, and a bare one is
// ideal. Pointing it at a live human checkout is supported, and is not
// recommended.
//
// # Revisions are blob object ids
//
// A [core.Revision] from this package is a blob object id in lowercase hex,
// exactly as git prints it. Compare-and-swap is therefore per document: two
// editors working on different documents never collide, and a Put fails with
// [core.ErrConflict] only when the bytes at that path changed. A commit id would
// have made every concurrent write to the repository look like a conflict on
// every document.
//
// Two consequences are worth stating rather than discovering. A blob id repeats
// when the bytes repeat, so a document reverted to earlier content reports the
// earlier revision again, and a revision does not identify a unique commit. And a
// Put whose serialized bytes equal what is stored is a no-op: no blob is written,
// no commit is made, the ref does not move, and the revision the caller already
// held is returned. Writing an empty commit instead would put a change that
// changed nothing into the user's history.
//
// # Writing
//
// Put and Delete each build a tree, commit it and publish it with
// "git update-ref <ref> <new> <old>", which is a genuine atomic compare-and-swap;
// on an unborn branch the old value is the empty string, which is git's
// create-only form, so two writers racing to make the first commit behave like
// two writers racing on anything else.
//
// A failed publish is not by itself a conflict. The ref moves whenever anyone
// commits anything, so publishing is a bounded retry with a jittered backoff:
// re-resolve the branch, re-read the document's own blob id, and if it still
// matches what the caller supplied, rebuild on the new tip and try again. Only a
// changed blob id is [core.ErrConflict]. The bound is [maxPublishAttempts];
// exhausting it returns a wrapped error naming the count, because an unbounded
// retry under sustained write load is a hang wearing a correctness costume.
//
// Writes from one *Repository take turns, which is an efficiency measure and not
// a safety one -- a branch ref admits one write at a time whatever anybody does,
// so a process that lets its own writers stampede it merely rebuilds the same
// trees repeatedly. The compare-and-swap is still what makes concurrent writing
// correct, and it is what handles the writers no queue can reach: another
// Micawber, a CI job, or the user at a shell.
//
// Put recovers the stored document's [github.com/shutx-net/micawber/markdown.Layout]
// from the object it has to read anyway for the revision check, and re-emits into
// it. That is what lets Layout stay out of [core.ContentRepository] while a
// document with CRLF terminators, a byte-order mark or TOML front matter survives
// a Get, an edit and a Put byte for byte. A create has no previous shape and gets
// the canonical one; so does an update over bytes that will not parse, since a
// damaged file has no shape to preserve.
//
// # Cancellation
//
// Every operation takes a context and honours it. Publishing is the last step of
// a write and the only visible one, so a cancelled Put either published or did
// not: there is no state in which half a document is readable. What it can leave
// behind is unreferenced objects, which nothing points at and "git gc" reclaims.
//
// The runner cancels with SIGTERM rather than the runtime's default SIGKILL, so
// that git's own handler runs and removes any lock file it holds. A SIGKILL from
// outside Micawber -- an operator, an OOM kill, a power cut -- can still strand a
// "<ref>.lock" that blocks every later writer including plain git. That is Git's
// own failure mode, and its answer is that a human removes the file.
//
// # Paths
//
// A [core.ContentPath] is joined onto the content root with
// [core.Collection.Join], which validates before it joins, so a path can never
// address anything outside the root. Paths reach git on standard input wherever a
// plumbing command accepts them there, which is nearly everywhere; where one must
// be an argument it is wrapped as a ":(top,literal)" pathspec, which turns off
// the wildcard matching that would otherwise make "posts/a*.md" mean every
// document it happens to match, and makes it relative to the repository root
// rather than to the directory [Open] was given. The runner additionally refuses
// any operand that begins with a dash, which core accepts and is right to accept:
// it is a legal filename, and hazardous only to something building argument
// vectors.
//
// List skips what it cannot honestly report: a path core cannot represent, a
// submodule, and a symbolic link, none of which is a document worth offering.
// Failing the whole listing instead would make one such file, committed by
// anyone ever, enough to make a collection unlistable. Get is one process and so
// sees objects rather than tree entries: asked for a link's path by name it
// returns the link's target text, and a Put over that path replaces the link with
// an ordinary file. Neither is reachable from a listing.
//
// # Errors
//
// Failures match errors.Is against [core.ErrNotFound], [core.ErrExists],
// [core.ErrConflict] and [core.ErrInvalid]. [core.ErrUnsupported] is not used:
// everything the two interfaces ask for, a Git backend can do.
//
// Classification reads the structured signals plumbing gives -- a "missing" line
// from cat-file, an object type, an exit status -- and never git's prose, which
// is localised into the user's language and is not a compatibility promise. The
// error carries the argument vector and git's standard error for a human, and
// never a byte of the document or of the environment.
//
// # History
//
// History and GetRevision are one path-filtered "git log" plus one
// "git cat-file --batch". History does not pass --follow, so a document's history
// stops at a rename: rename detection is a similarity heuristic, and a revision
// list that changes shape because Git's guess changed is a poor thing to build
// compare-and-swap on.
//
// GetRevision checks that the revision it is given appears in the history of the
// path it is given, before reading anything. Without that check it would be an
// arbitrary-object read oracle wearing a path argument: "git cat-file blob" will
// return any object in the database, including one from another branch, one
// outside the content root, or one fetched from a remote and never checked out.
package git
