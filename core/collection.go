package core

import "strings"

// Collection is a validated relative directory that bounds listing and path
// joining. Its zero value is the content root, which contains every valid
// ContentPath, so a caller that wants everything can pass Collection{} without
// a special constructor.
//
// A Collection is comparable and can be used as a map key.
type Collection struct {
	p string
}

// NewCollection validates s and returns it as a Collection.
//
// s follows the same rules as a content path except that it names a directory:
// non-empty, relative, already cleaned, free of control characters and ".."
// segments, and without a ".git" segment. The single exception is ".", the
// spelling String and MarshalText use for the root, which yields the root
// collection. The empty string is rejected, because it is far more often an
// unset field than a deliberate "everything".
//
// The returned error unwraps to ErrInvalid.
func NewCollection(s string) (Collection, error) {
	const kind = "collection"

	if s == "." {
		return Collection{}, nil
	}
	if err := validateRelPath(kind, s, maxContentPathBytes); err != nil {
		return Collection{}, err
	}
	if err := rejectGitSegment(kind, s); err != nil {
		return Collection{}, err
	}
	return Collection{p: s}, nil
}

// String returns the collection in slash form, or "." for the root.
func (c Collection) String() string {
	if c.p == "" {
		return "."
	}
	return c.p
}

// IsRoot reports whether c is the content root, which contains every path.
func (c Collection) IsRoot() bool { return c.p == "" }

// Contains reports whether p lies within c, at any depth. The comparison is
// made on segment boundaries, so the collection "posts" does not contain
// "postsx/a.md". The zero ContentPath is in no collection.
func (c Collection) Contains(p ContentPath) bool {
	if p.IsZero() {
		return false
	}
	if c.IsRoot() {
		return true
	}
	return strings.HasPrefix(p.p, c.p+"/")
}

// Join validates rel as a path relative to c and returns the resulting
// ContentPath.
//
// rel is validated before it is joined, not after, so a collection can never
// produce a path outside itself: "posts".Join("../secrets.md") and
// "posts".Join("a/../../b.md") both fail rather than cleaning down to an
// unrelated document. The joined result is validated again as a ContentPath,
// so Join also cannot produce a path the repository would reject.
//
// The returned error unwraps to ErrInvalid.
func (c Collection) Join(rel string) (ContentPath, error) {
	if err := validateRelPath("content path", rel, maxContentPathBytes); err != nil {
		return ContentPath{}, err
	}
	if c.IsRoot() {
		return NewContentPath(rel)
	}
	return NewContentPath(c.p + "/" + rel)
}

// Rel returns p relative to c, and whether p is in c at all. For the root
// collection it returns the whole path.
func (c Collection) Rel(p ContentPath) (string, bool) {
	if !c.Contains(p) {
		return "", false
	}
	if c.IsRoot() {
		return p.p, true
	}
	return strings.TrimPrefix(p.p, c.p+"/"), true
}

// MarshalText implements encoding.TextMarshaler. The root marshals to ".".
func (c Collection) MarshalText() ([]byte, error) { return []byte(c.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler. It validates the text the
// same way NewCollection does, accepts "." as the root, and leaves the
// receiver unchanged on failure.
func (c *Collection) UnmarshalText(b []byte) error {
	parsed, err := NewCollection(string(b))
	if err != nil {
		return err
	}
	*c = parsed
	return nil
}
