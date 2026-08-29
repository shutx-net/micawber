package core

import (
	"context"
	"io"
)

// AssetStore holds binary assets. Assets are deliberately not in the content
// repository: images and downloads do not belong in Git history, and the store
// behind this interface may be a local directory or an S3-compatible object
// store. No implementation lives in this package.
//
// # Obligations
//
// Implementations must honour ctx cancellation and return its error, and must
// return errors that satisfy errors.Is against [ErrNotFound] and [ErrInvalid].
// Nothing here exposes a public or signed URL: delivery, including any CDN, is
// a separate concern from storage.
type AssetStore interface {
	// Put writes the bytes read from r as the object described by a, and
	// returns what the store now holds. The bytes travel as a reader rather
	// than in the Asset because an asset can be far larger than anything that
	// belongs in a domain value.
	//
	// Put reads r to EOF. a.Size may be SizeUnknown, in which case the store
	// discovers the length as it writes. Put overwrites an existing object at
	// the same key: assets have no compare-and-swap, because the content
	// repository is where concurrent editing happens.
	Put(ctx context.Context, a Asset, r io.Reader) (AssetRef, error)

	// Get opens the object at key for reading. The caller closes the returned
	// reader. A missing object is an error matching ErrNotFound.
	Get(ctx context.Context, key AssetKey) (io.ReadCloser, error)

	// Stat returns what the store knows about the object at key without
	// reading it. A missing object is an error matching ErrNotFound.
	Stat(ctx context.Context, key AssetKey) (AssetRef, error)

	// Delete removes the object at key. Deleting something that is not there
	// is an error matching ErrNotFound, matching ContentRepository.Delete.
	Delete(ctx context.Context, key AssetKey) error
}
