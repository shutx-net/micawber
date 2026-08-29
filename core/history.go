package core

import (
	"context"
	"time"
)

// RevisionInfo describes one stored version of a document.
type RevisionInfo struct {
	// Revision identifies the version, and can be passed to
	// ContentHistory.GetRevision.
	Revision Revision
	// Author is who made the change, as far as the backend recorded it.
	Author Author
	// Time is when the change was made.
	Time time.Time
	// Message is the change description the author gave.
	Message string
}

// ContentHistory is an optional capability a ContentRepository may also
// implement. Callers detect it with a type assertion:
//
//	if h, ok := repo.(ContentHistory); ok {
//		infos, err := h.History(ctx, p, 20)
//	}
//
// History is the main thing Git gives Micawber for free, but a plain directory
// cannot provide it. Keeping it out of [ContentRepository] means a store with
// no history is simply not a ContentHistory, rather than an implementation
// obliged to write ErrUnsupported stubs.
type ContentHistory interface {
	// History returns the revisions of p, most recent first. A limit above
	// zero caps the number of entries; zero or less means no limit.
	// A document that does not exist is an error matching ErrNotFound.
	History(ctx context.Context, p ContentPath, limit int) ([]RevisionInfo, error)

	// GetRevision returns p as it stood at rev. A revision that the backend
	// does not hold is an error matching ErrNotFound.
	GetRevision(ctx context.Context, p ContentPath, rev Revision) (Content, error)
}
