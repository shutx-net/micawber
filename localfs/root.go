package localfs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/shutx-net/micawber/core"
)

// The errors this file classifies raw filesystem failures into. Nothing outside
// this file names a syscall.Errno or an *os.PathError, so nothing outside this
// file can leak a path into a message by wrapping one.
var (
	// errNoEntry is a name, or a component of one, that is not there.
	errNoEntry = errors.New("no such file or directory")
	// errNotDir is a component of a name that is not a directory, which is what
	// an ancestor of the key being a regular file looks like.
	errNotDir = errors.New("a component of the name is not a directory")
	// errIsDir is a directory where a file was wanted.
	errIsDir = errors.New("a directory occupies the name")
	// errNameTaken is a create or a rename refused because the name exists.
	errNameTaken = errors.New("the name is already taken")
	// errEscape is a name that resolved outside the root.
	errEscape = errors.New("the name escapes the root")
	// errNotRegular is an entry that exists and is not a regular file.
	errNotRegular = errors.New("not a regular file")
)

// The modes an object and a directory are created with, both subject to the
// process umask, which is what keeps the operator in control of the default.
// They are what a directory a web server may also read wants.
//
// No option is offered to change either: configuration is a subsystem this
// phase does not own, and a knob nothing has asked for is a knob with no
// caller to justify its shape.
const (
	objectMode = 0o644
	dirMode    = 0o755
)

// The temporary file a Put writes before it publishes.
//
// The name is unguessable rather than reserved. A key that happens to match the
// pattern is legal and the store does not refuse it, because the only hazard --
// a caller storing an object at the exact name a concurrent Put is about to
// rename away -- needs 128 bits from crypto/rand predicted, and O_EXCL means a
// name that does collide is retried rather than overwritten. Reserving a prefix
// instead would add a validation rule with no attack behind it; git, rsync and
// Maildir all use unreserved same-directory temporary files for the same
// reason.
const (
	tempPrefix    = ".micawber-"
	tempSuffix    = ".tmp"
	tempNameBytes = 16
	tempAttempts  = 8
)

// escapeMessage is what os.Root says when a name resolves outside the root.
//
// It is matched as text because Go does not export a sentinel for it. The match
// is belt and braces rather than a guess: an escape is the only refusal os.Root
// makes that carries no syscall.Errno, so both signals have to agree. A Go
// release that reworded it would leave the failure unclassified — a wrapped
// error and a 500 — rather than turning it into something permissive, and
// TestEveryOperationRefusesAnEscapingKeyIdentically is what would notice.
const escapeMessage = "path escapes from parent"

// fsRoot is the whole of this package's access to the filesystem: one *os.Root,
// opened at the store's directory and held for its lifetime.
//
// Every path operation is a method on it, and the key is never joined onto the
// root by this package. That is what makes the check and the open fused per
// component, which a joined path validated with filepath.EvalSymlinks can never
// be: the symlink can be planted between the check and the open.
type fsRoot struct {
	r *os.Root
}

// openRoot opens dir as a root. It fails when dir does not exist or is not a
// directory, and creates nothing.
func openRoot(dir string) (*fsRoot, error) {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, classify(err)
	}
	return &fsRoot{r: r}, nil
}

// close releases the root descriptor. Readers already handed out are not
// affected: an open file's descriptor is independent of the root's, which is
// what makes it safe to close a Store while a response is still streaming.
func (fr *fsRoot) close() error {
	if err := fr.r.Close(); err != nil {
		return classify(err)
	}
	return nil
}

// objectInfo is what this package learns about an entry from the filesystem.
//
// It exists so that root.go can hand back a value rather than an fs.FileInfo:
// no other file in the package can then name a filesystem type even by
// accident, which is what makes the confinement guard mean something.
type objectInfo struct {
	// size is the entry's length in bytes.
	size int64
	// modTime is when the entry was last modified.
	modTime time.Time
	// regular reports whether the entry is a regular file, as opposed to a
	// directory, a symbolic link, a FIFO, a socket or a device node.
	regular bool
}

// infoOf reduces an fs.FileInfo to the three things this package uses.
func infoOf(fi os.FileInfo) objectInfo {
	return objectInfo{size: fi.Size(), modTime: fi.ModTime(), regular: fi.Mode().IsRegular()}
}

// relName converts a key into a name relative to the root.
//
// It is the only conversion in the package, and it is not a join: the key never
// meets the root's own path, which is what keeps a key usable when the root is
// deep. Measured, a legal 999-byte key under a 4,069-byte root works through
// os.Root and fails with ENAMETOOLONG through a joined path, so without this the
// store would have a key-length limit that depended on where the operator put
// the directory.
func relName(key core.AssetKey) string {
	return filepath.FromSlash(key.String())
}

// lstat reports on the entry at name without following a symbolic link at its
// final component.
//
// Lstat rather than Stat is the whole of the symlink rule: a link at a key is
// reported as what it is, so the store's answer never depends on what is on the
// other side of it.
func (fr *fsRoot) lstat(name string) (objectInfo, error) {
	fi, err := fr.r.Lstat(name)
	if err != nil {
		return objectInfo{}, classify(err)
	}
	return infoOf(fi), nil
}

// openObject opens the entry at key for reading and binds it to ctx.
//
// The mode is checked twice. The caller has already refused anything the
// directory says is not a regular file, and this re-checks the descriptor it
// actually got, so a regular file swapped for a directory between the two calls
// is caught rather than failing mid-stream. The one case the second check
// cannot cover is a FIFO swapped in between them, because open(2) blocks before
// there is a descriptor to interrogate; closing that window needs
// O_NOFOLLOW|O_NONBLOCK, which is a per-platform constant and therefore a
// second build-tagged file for the confinement guard to bless. The residual is
// a hang, never an escape, and it needs an adversary who can already write into
// the directory.
func (fr *fsRoot) openObject(ctx context.Context, key core.AssetKey) (*object, error) {
	f, err := fr.r.Open(relName(key))
	if err != nil {
		return nil, classify(err)
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, classify(err)
	}
	if !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, errNotRegular
	}
	return &object{ctx: ctx, key: key, f: f}, nil
}

// object is the reader Get hands back: one descriptor and the context the call
// was given.
//
// It adds no buffer. io.Copy supplies its own 32 KiB, and a second layer would
// only make the cancellation granularity worse while looking like an
// optimisation.
type object struct {
	ctx context.Context
	key core.AssetKey
	f   *os.File
}

// Read returns the context's error once it is done, which is what bounds a
// cancelled stream to one io.Copy buffer. A read failure is rebuilt around the
// key: the error an *os.File gives carries its own name, which is the joined
// absolute path.
func (o *object) Read(p []byte) (int, error) {
	if err := o.ctx.Err(); err != nil {
		return 0, sentinelf(err, nil, "read %q: the context is done", o.key)
	}
	n, err := o.f.Read(p)
	if err == nil || errors.Is(err, io.EOF) {
		return n, err
	}
	return n, readError("read", o.key, classify(err))
}

// Close releases the descriptor. It is the only thing that does: cancelling the
// context stops the stream and frees nothing.
func (o *object) Close() error {
	if err := o.f.Close(); err != nil {
		return readError("close", o.key, classify(err))
	}
	return nil
}

// relDir returns the key's parent as a name relative to the root, and false
// when the key names something directly in the root.
func relDir(key core.AssetKey) (string, bool) {
	dir := filepath.Dir(relName(key))
	if dir == "." {
		return "", false
	}
	return dir, true
}

// mkdirAll creates name and any missing parents. It is idempotent under EEXIST,
// so two Puts to sibling keys under a new prefix need no coordination.
func (fr *fsRoot) mkdirAll(name string) error {
	if err := fr.r.MkdirAll(name, dirMode); err != nil {
		return classify(err)
	}
	return nil
}

// remove unlinks name.
func (fr *fsRoot) remove(name string) error {
	if err := fr.r.Remove(name); err != nil {
		return classify(err)
	}
	return nil
}

// createTemp creates a new empty file in dir, which is a directory inside the
// root or "" for the root itself.
//
// Same directory, not a shared temporary directory at the root: rename must not
// cross a filesystem boundary, and a subdirectory of the root can be a separate
// mount, so same-directory temporary files make EXDEV impossible.
func (fr *fsRoot) createTemp(dir string) (*tempFile, error) {
	var last error
	for range tempAttempts {
		var raw [tempNameBytes]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return nil, err
		}
		name := filepath.Join(dir, tempPrefix+hex.EncodeToString(raw[:])+tempSuffix)

		f, err := fr.r.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, objectMode)
		if err == nil {
			return &tempFile{fr: fr, dir: dir, name: name, f: f}, nil
		}
		last = classify(err)
		if !errors.Is(last, errNameTaken) {
			return nil, last
		}
	}
	return nil, last
}

// tempFile is a Put in progress: bytes on disk under a name no key can reach,
// waiting to be published or removed.
type tempFile struct {
	fr   *fsRoot
	dir  string
	name string
	f    *os.File

	closed    bool
	committed bool
}

// Write appends to the temporary file.
func (t *tempFile) Write(p []byte) (int, error) {
	n, err := t.f.Write(p)
	if err != nil {
		return n, classify(err)
	}
	return n, nil
}

// commit makes the bytes durable and publishes them at name, returning what the
// object looks like from the filesystem's point of view.
//
// The order is the whole of the guarantee: fstat for the modification time,
// fsync the bytes, close, rename over the key, and then flush the directory.
// rename(2) over an existing name is atomic, so a reader either opens the old
// object or the new one and never a mixture. The information comes from an
// fstat of the temporary file rather than from a stat of the key afterwards,
// which matters under a race: with last-writer-wins and no compare-and-swap, a
// stat after the rename can describe another writer's object, and Put would
// then return a ref for bytes it did not write.
func (t *tempFile) commit(name string) (objectInfo, error) {
	fi, err := t.f.Stat()
	if err != nil {
		return objectInfo{}, classify(err)
	}
	if err := t.f.Sync(); err != nil {
		return objectInfo{}, classify(err)
	}
	t.closed = true
	if err := t.f.Close(); err != nil {
		return objectInfo{}, classify(err)
	}
	if err := t.fr.r.Rename(t.name, name); err != nil {
		return objectInfo{}, classify(err)
	}
	t.committed = true
	t.fr.syncDir(t.dir)
	return infoOf(fi), nil
}

// abort removes the temporary file.
//
// It runs on the failure path only, guarded by commit's own flag, rather than
// in an unconditional defer, so a successful Put issues no pointless unlink.
// Errors are ignored because there is nothing useful left to say: the Put has
// already failed for a reason the caller is about to be told.
func (t *tempFile) abort() {
	if t.committed {
		return
	}
	if !t.closed {
		t.closed = true
		_ = t.f.Close()
	}
	_ = t.fr.r.Remove(t.name)
}

// syncDir flushes the directory entry a rename created, so that the new name
// survives a power cut and not only the bytes behind it.
//
// Its failure is ignored on purpose, and not out of laziness: by the time it
// runs the rename has already happened and the object is visible to every
// reader, so returning an error would report a failure for a Put that
// succeeded. A directory handle does not accept a flush on every platform.
func (fr *fsRoot) syncDir(dir string) {
	if dir == "" {
		dir = "."
	}
	d, err := fr.r.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// classify reduces a raw filesystem failure to one of this package's own
// errors.
//
// Everything it returns is either a sentinel above or a bare syscall.Errno,
// whose message names the condition and never a path. An unrecognised errno —
// EACCES and EIO among them — is returned as itself, so that it reaches a
// caller wrapped and unclassified rather than being forced into a sentinel that
// would tell an HTTP layer to blame the request.
func classify(err error) error {
	if err == nil {
		return nil
	}
	cause := reduce(err)

	var errno syscall.Errno
	if errors.As(cause, &errno) {
		switch errno {
		case syscall.ENOENT:
			return errNoEntry
		case syscall.ENOTDIR:
			return errNotDir
		case syscall.EISDIR:
			return errIsDir
		case syscall.EEXIST, syscall.ENOTEMPTY:
			return errNameTaken
		}
		return errno
	}
	if isEscape(cause) {
		return errEscape
	}
	return cause
}

// reduce strips the *os.PathError and *os.LinkError wrappers, which are the two
// shapes that carry a filesystem path, leaving the errno or the bare refusal
// underneath.
//
// It loops because os nests them: MkdirAll through an escaping symlink returns
// a *os.PathError whose Err is another *os.PathError.
func reduce(err error) error {
	for {
		var pathErr *os.PathError
		var linkErr *os.LinkError
		switch {
		case errors.As(err, &pathErr) && pathErr.Err != nil:
			err = pathErr.Err
		case errors.As(err, &linkErr) && linkErr.Err != nil:
			err = linkErr.Err
		default:
			return err
		}
	}
}

// isEscape reports whether err is os.Root's refusal of a name that resolved
// outside the root.
func isEscape(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return false
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		if e.Error() == escapeMessage {
			return true
		}
	}
	return false
}
