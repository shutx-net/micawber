package localfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"strings"

	"github.com/shutx-net/micawber/core"
)

// The capability this store provides, asserted where the compiler can see it.
// TestStoreSatisfiesTheCoreInterface says the same thing in a form a reader can
// grep for.
var _ core.AssetStore = (*Store)(nil)

// Store is a [core.AssetStore] backed by a local filesystem directory.
//
// It holds one *os.Root, opened at [Open] and never replaced, and a media-type
// table written once at [Open] and read-only thereafter. There is nothing else,
// so a *Store is safe for concurrent use by multiple goroutines and holds no
// lock of its own: the guarantees under concurrency are the filesystem's, which
// is the only serialisation point every writer of the directory shares — other
// processes, the operator at a shell, and a second Micawber included.
type Store struct {
	// root is the whole of this store's access to the filesystem.
	root *fsRoot
	// media holds the operator's additions to the shipped media-type table. It
	// is never written after Open.
	media map[string]string
}

// options is the configuration [Open] assembles before anything runs.
type options struct {
	media map[string]string
}

// Option configures [Open]. Options are applied and validated before the
// directory is opened, so a misconfiguration is reported without touching it.
type Option func(*options) error

// WithMediaTypes extends and overrides the media types this store derives from
// a key's extension. Each entry maps a lower-case dotted extension, such as
// ".glb", to a media type that parses, such as "model/gltf-binary".
//
// It exists because the table this package ships is fixed rather than taken
// from the host, which is what makes the store's answer the same everywhere;
// the cost of that is that a deployment storing a format the table does not
// know has no way to say so short of patching the source. This is that way.
//
// A malformed entry is an error matching [core.ErrInvalid], reported by [Open].
func WithMediaTypes(m map[string]string) Option {
	return func(o *options) error {
		if err := checkMediaTypes(m); err != nil {
			return err
		}
		if o.media == nil {
			o.media = make(map[string]string, len(m))
		}
		maps.Copy(o.media, m)
		return nil
	}
}

// Open opens the asset store held in the directory dir.
//
// The directory must already exist and must be a directory; Open creates
// nothing, because writing into a directory somebody did not mean to create is
// the wrong default for a tool that manages their content. It performs no probe
// of any kind — in particular no case-sensitivity test — since a probe means
// writing a file into the operator's directory as a side effect of opening it.
//
// dir is resolved once, into a descriptor held for the store's lifetime. Two
// consequences are worth knowing: renaming the directory does not break the
// Store, and replacing the directory with a different one does not redirect it.
//
// The directory is a trust boundary rather than a sandbox. Anyone who can
// create entries in it can already write any bytes there, so it must be
// Micawber's; see the package documentation for what that precondition does and
// does not buy.
func Open(ctx context.Context, dir string, opts ...Option) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, sentinelf(err, nil, "open: the context is done")
	}

	var o options
	for _, opt := range opts {
		if opt == nil {
			return nil, sentinelf(core.ErrInvalid, nil, "open %q: a nil Option was given", dir)
		}
		if err := opt(&o); err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(dir) == "" {
		return nil, sentinelf(core.ErrInvalid, nil, "open: the directory is empty")
	}

	root, err := openRoot(dir)
	if err != nil {
		return nil, sentinelf(core.ErrInvalid, err, "open %q: not a usable directory", dir)
	}
	return &Store{root: root, media: o.media}, nil
}

// Close releases the store's directory descriptor.
//
// It is not part of [core.AssetStore], which describes what a caller does with
// a store rather than who owns its lifetime. Readers already handed out by
// [Store.Get] are not closed and keep working: an open file's descriptor is
// independent of the root's, which is what makes it safe to close a Store while
// a response is still streaming. Every later call on the Store fails.
func (s *Store) Close() error {
	return s.root.close()
}

// Put writes the bytes read from r as the object described by a, and returns
// what it wrote.
//
// It is atomic and durable. The bytes go to a temporary file in the key's own
// directory, are flushed to the platter, and are then renamed over the key,
// with the containing directory flushed afterwards on a best-effort basis; a
// reader therefore either opens the old object or the new one, and never a
// mixture. Put overwrites an existing object, because [core.AssetStore] says
// assets have no compare-and-swap.
//
// a.Size may be [core.SizeUnknown], in which case Put reads to EOF and records
// what it read. When a.Size is a length and the stream disagrees with it in
// either direction, nothing is published and the error matches
// [core.ErrInvalid]. In the too-long direction Put stops one byte past the
// declared size rather than draining the stream, so a declaration of one byte
// cannot be used to make the store write ten gigabytes; that is the one place
// Put does not read r to EOF.
//
// a.ContentType is not stored and not reported. The returned ref carries the
// content type derived from the key's extension, which is what a later
// [Store.Stat] will report too — there is nowhere to have kept the caller's
// answer that a file dropped into the directory by hand would also have.
//
// The returned ref is a description of the bytes this call wrote, taken from
// the temporary file before it was published. Under two racing Puts both refs
// are truthful and one of them describes an object that is no longer at the
// key, which is the only honest thing either can report where there is no
// compare-and-swap.
//
// A failed or cancelled Put removes its temporary file and leaves the key as it
// was, so a cancelled replacement is not a lost object. A killed process leaves
// one temporary file behind and nothing else; see the package documentation for
// whose job that is.
func (s *Store) Put(ctx context.Context, a core.Asset, r io.Reader) (core.AssetRef, error) {
	const op = "put"

	if err := ctx.Err(); err != nil {
		return core.AssetRef{}, sentinelf(err, nil, "%s %q: the context is done", op, a.Key)
	}
	if err := a.Validate(); err != nil {
		return core.AssetRef{}, err
	}
	if err := checkKey(a.Key); err != nil {
		return core.AssetRef{}, err
	}
	if r == nil {
		return core.AssetRef{}, sentinelf(core.ErrInvalid, nil, "%s %q: the reader is nil", op, a.Key)
	}

	dir, nested := relDir(a.Key)
	if nested {
		if err := s.root.mkdirAll(dir); err != nil {
			return core.AssetRef{}, writeError(op, a.Key, err)
		}
	}

	tmp, err := s.root.createTemp(dir)
	if err != nil {
		return core.AssetRef{}, writeError(op, a.Key, err)
	}

	digest, err := copyInto(ctx, tmp, a, r)
	if err != nil {
		tmp.abort()
		return core.AssetRef{}, err
	}
	info, err := tmp.commit(relName(a.Key))
	if err != nil {
		tmp.abort()
		return core.AssetRef{}, writeError(op, a.Key, err)
	}

	return core.AssetRef{
		Key:         a.Key,
		Size:        info.size,
		ContentType: mediaTypeFor(a.Key, s.media),
		Digest:      digest,
		ModTime:     info.modTime,
	}, nil
}

// copyInto streams r into tmp, hashing as it goes, and checks the result
// against the length the caller declared.
//
// The reader is wrapped twice and both wrappers earn their place. The context
// check bounds a cancelled Put to one io.Copy buffer, measured at 32 KiB,
// instead of to the length of the stream. The io.LimitReader bound is what
// stops a declared size being an amplification primitive: at most one byte more
// than declared is ever read, and that byte is what the disagreement is
// detected on.
func copyInto(ctx context.Context, tmp *tempFile, a core.Asset, r io.Reader) (core.Digest, error) {
	const op = "put"

	src := r
	bounded := a.Size != core.SizeUnknown
	if bounded {
		src = io.LimitReader(src, a.Size+1)
	}

	sum := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, sum), &ctxReader{ctx: ctx, r: src})
	if err != nil {
		return "", fmt.Errorf("localfs: %s %q: %w", op, a.Key, err)
	}
	if bounded && written != a.Size {
		return "", sentinelf(core.ErrInvalid, nil,
			"%s %q: the stream held %d bytes, and the caller declared %d", op, a.Key, written, a.Size)
	}

	digest, err := core.NewDigest("sha256", hex.EncodeToString(sum.Sum(nil)))
	if err != nil {
		return "", fmt.Errorf("localfs: %s %q: %w", op, a.Key, err)
	}
	return digest, nil
}

// ctxReader stops a stream once the context is done.
//
// It is a reader rather than a goroutine watching ctx.Done() for the same
// reason the object reader is: a goroutine per call would have to be cleaned up
// on every path, and the granularity a check per buffer gives is already one
// io.Copy buffer.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// Get opens the object at key for reading. The caller closes the returned
// reader, and until they do it holds one file descriptor.
//
// The reader is bound to ctx: once ctx is done, Read returns its error, which
// bounds a cancelled stream to one io.Copy buffer rather than to the length of
// the object. Cancellation stops the stream and frees nothing — only Close
// releases the descriptor — so a caller who wants the stream to outlive the
// call must pass a context that outlives it, the same convention
// database/sql.Rows follows.
//
// The reader is independent of the Store. Closing the Store does not close it,
// which is what makes it safe to shut down while a response is still streaming;
// a Delete of the key does not disturb it either, because unlink removes the
// directory entry and the bytes survive until the last descriptor closes.
//
// Anything at key that is not a regular file is [core.ErrNotFound], and Get
// never opens it: a FIFO would block open(2) until a writer appeared, and no
// context can interrupt that.
func (s *Store) Get(ctx context.Context, key core.AssetKey) (io.ReadCloser, error) {
	const op = "get"

	if _, err := s.resolve(ctx, op, key); err != nil {
		return nil, err
	}

	obj, err := s.root.openObject(ctx, key)
	if err != nil {
		return nil, readError(op, key, err)
	}
	return obj, nil
}

// resolve is the three steps Get, Stat and Delete all begin with: refuse a done
// context, apply this store's key rules, and require the entry to be a regular
// file.
//
// The mode check is one cheap syscall on the path that already needs the size
// and the modification time, and it is what keeps a FIFO, a device node, a
// directory or a symbolic link from ever being opened.
func (s *Store) resolve(ctx context.Context, op string, key core.AssetKey) (objectInfo, error) {
	if err := ctx.Err(); err != nil {
		return objectInfo{}, sentinelf(err, nil, "%s %q: the context is done", op, key)
	}
	if err := checkKey(key); err != nil {
		return objectInfo{}, err
	}

	info, err := s.root.lstat(relName(key))
	if err != nil {
		return objectInfo{}, readError(op, key, err)
	}
	if !info.regular {
		return objectInfo{}, readError(op, key, errNotRegular)
	}
	return info, nil
}

// Stat reports what the store knows about the object at key without reading
// it: the size and modification time from the directory entry, and a content
// type derived from the key's extension.
//
// The digest is always the zero value. There is nowhere a digest could have
// been stored that a file dropped into the directory by hand would also have,
// and recomputing one would make an O(1) call O(size) — a store whose Stat
// reads the object is not the Stat [core.AssetStore] describes.
//
// Anything at the key that is not a regular file is [core.ErrNotFound]: a
// directory, a symbolic link wherever it points, a FIFO, a socket or a device
// node. So is a key whose ancestor is a regular file, and so is a name that
// resolves outside the directory.
func (s *Store) Stat(ctx context.Context, key core.AssetKey) (core.AssetRef, error) {
	info, err := s.resolve(ctx, "stat", key)
	if err != nil {
		return core.AssetRef{}, err
	}
	return core.AssetRef{
		Key:         key,
		Size:        info.size,
		ContentType: mediaTypeFor(key, s.media),
		ModTime:     info.modTime,
	}, nil
}

// Delete removes the object at key. Deleting something that is not there is an
// error matching [core.ErrNotFound], which is what [core.AssetStore] asks for.
//
// It removes regular files only. A directory at the key is [core.ErrNotFound]
// and stays where it is, including an empty one — os.Root.Remove would take an
// empty directory happily, so the check before it is load-bearing rather than
// decorative. So is a symbolic link, wherever it points: Delete must not become
// a way to unlink something Get would refuse to read.
//
// Empty parent directories are never pruned. Walking up from img/2026/08 to
// remove them can take a directory a concurrent Put has just created and not
// yet renamed into, which would turn that writer's rename into ENOENT and fail
// a Put that should have succeeded. An empty directory is inert and visible,
// and the operator can remove it.
//
// A reader already streaming the object is unaffected: unlink removes the
// directory entry and the bytes survive until the last descriptor closes, so
// the disk space comes back when the reader closes rather than when Delete
// returns.
func (s *Store) Delete(ctx context.Context, key core.AssetKey) error {
	const op = "delete"

	if _, err := s.resolve(ctx, op, key); err != nil {
		return err
	}
	if err := s.root.remove(relName(key)); err != nil {
		// ENOENT here means another writer won the race between the check and
		// the removal, which is the same outcome as the key never being there.
		return readError(op, key, err)
	}
	return nil
}
