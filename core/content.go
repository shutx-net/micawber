package core

import (
	"fmt"
	"slices"
)

// Revision is an opaque token identifying a stored version of a document.
//
// Only the repository that issued a revision may interpret it: a Git backend
// might use an object id, a filesystem store a size and modification time, an
// object store an ETag. Callers compare revisions for equality and pass them
// back for compare-and-swap, and must not parse them.
//
// The zero Revision means "no revision": on Put it asks for a create, on
// Delete it asks for an unconditional delete.
type Revision string

// IsZero reports whether r is the empty revision.
func (r Revision) IsZero() bool { return r == "" }

// Content is a document: where it lives, its front matter, its body and the
// revision it was read at.
//
// Body holds the bytes after the front-matter block, verbatim. Content
// deliberately does not also keep a copy of the whole file: the file is
// reconstructible from the delimiters, FrontMatter.Raw and Body, and storing
// it twice would let the two copies disagree.
type Content struct {
	// Path is where the document lives, relative to the content root.
	Path ContentPath
	// FrontMatter is the document's front-matter block, which may be empty.
	FrontMatter FrontMatter
	// Body is the Markdown after the front matter, byte for byte as authored.
	Body []byte
	// Revision is the revision this content was read at. It is empty for a
	// document that has not been stored yet.
	Revision Revision
}

// Validate reports whether c is well formed: it has a path, and its front
// matter passes FrontMatter.Validate. The body is never inspected; its content
// is the author's business.
//
// The returned error unwraps to ErrInvalid.
func (c Content) Validate() error {
	if c.Path.IsZero() {
		return invalidf("content", "", "has no path")
	}
	if err := c.FrontMatter.Validate(); err != nil {
		return fmt.Errorf("content %q: %w", c.Path, err)
	}
	return nil
}

// Clone returns a copy that shares no Body slice or front-matter map with c,
// so a repository may hand out content the caller is free to mutate.
func (c Content) Clone() Content {
	return Content{
		Path:        c.Path,
		FrontMatter: c.FrontMatter.Clone(),
		Body:        slices.Clone(c.Body),
		Revision:    c.Revision,
	}
}

// ContentEntry is a document as it appears in a listing: enough to render an
// index or build a cache key, without reading or decoding the document.
type ContentEntry struct {
	// Path is where the document lives, relative to the content root.
	Path ContentPath
	// Revision is the revision the document currently has.
	Revision Revision
}
