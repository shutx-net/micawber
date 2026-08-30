package localfs

import (
	"errors"
	"fmt"

	"github.com/shutx-net/micawber/core"
)

// sentinelf wraps cause in a message and one of core's sentinels, so that the
// result matches errors.Is against both: the sentinel an HTTP layer maps to a
// status code, and the underlying condition a human needs in order to fix it.
//
// cause may be nil, for a refusal the store made without touching the
// filesystem.
func sentinelf(sentinel, cause error, format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	if cause == nil {
		return fmt.Errorf("localfs: %s: %w", msg, sentinel)
	}
	return fmt.Errorf("localfs: %s: %w: %w", msg, sentinel, cause)
}

// readError maps a failure from Get, Stat or Delete onto core's sentinels.
//
// Every way of not being an object collapses to ErrNotFound: the key is absent,
// an ancestor of it is a regular file, the entry is a directory, a symlink, a
// FIFO or a device, or the name resolved outside the root. The escape case
// belongs here rather than in a sentinel of its own because the store's answer
// for a key must not depend on what is on the other side of a link — under
// three distinguishable answers a caller could map the operator's symlinks and
// infer what exists outside the root.
//
// Anything else keeps no sentinel. A disk permission problem is not a caller
// mistake, and classifying it as one would tell an HTTP layer to return a 4xx
// for something only the operator can fix.
func readError(op string, key core.AssetKey, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, errNoEntry),
		errors.Is(err, errNotDir),
		errors.Is(err, errIsDir),
		errors.Is(err, errNotRegular),
		errors.Is(err, errEscape):
		return sentinelf(core.ErrNotFound, err, "%s %q", op, key)
	}
	return fmt.Errorf("localfs: %s %q: %w", op, key, err)
}

// writeError maps a failure from Put onto core's sentinels.
//
// It differs from readError in one row, and the difference is a structural
// property of a hierarchical namespace rather than an implementation choice: a
// directory can occupy "img/logo.png", and "img/logo.png" and
// "img/logo.png/thumb.png" cannot both exist, where in an object store they
// can. The request is well formed, so ErrInvalid would mislead an HTTP layer
// into a 400; no revision is involved, so ErrConflict does not apply. ErrExists
// is defined as a create at a path that is already taken, which is what has
// happened.
func writeError(op string, key core.AssetKey, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, errNotDir),
		errors.Is(err, errIsDir),
		errors.Is(err, errNameTaken):
		return sentinelf(core.ErrExists, err, "%s %q", op, key)
	case errors.Is(err, errNoEntry), errors.Is(err, errEscape):
		return sentinelf(core.ErrNotFound, err, "%s %q", op, key)
	}
	return fmt.Errorf("localfs: %s %q: %w", op, key, err)
}
