package core

import (
	"path"
	"strings"
	"unicode/utf8"
)

// maxContentPathBytes is the largest a whole content path may be. It is a
// deliberate ceiling rather than a filesystem limit: adapters may impose
// tighter ones at their own boundary.
const maxContentPathBytes = 4096

// maxPathSegmentBytes is the largest a single path segment may be, matching
// the limit common filesystems agree on.
const maxPathSegmentBytes = 255

// ContentPath is a validated, slash-separated path to a document, relative to
// the content root. The zero value is not a usable path; NewContentPath is the
// only way to obtain a valid one, so every adapter can rely on having received
// a path that cannot escape its root.
//
// A ContentPath is comparable and can be used as a map key.
type ContentPath struct {
	p string
}

// NewContentPath validates s and returns it as a ContentPath.
//
// s must be a non-empty, relative, already-cleaned slash path: valid UTF-8,
// free of control characters and backslashes, without a leading or trailing
// slash, without an empty, "." or ".." segment, without a drive prefix, and
// without a ".git" segment in any casing. Invalid input is rejected, never
// normalized:
// silently cleaning "posts/../../etc/passwd" into something valid would hide
// an attack rather than surface it. A caller that wants normalization can
// apply path.Clean itself and validate the result.
//
// The returned error unwraps to ErrInvalid.
func NewContentPath(s string) (ContentPath, error) {
	const kind = "content path"

	if err := validateRelPath(kind, s, maxContentPathBytes); err != nil {
		return ContentPath{}, err
	}
	if err := rejectGitSegment(kind, s); err != nil {
		return ContentPath{}, err
	}
	return ContentPath{p: s}, nil
}

// String returns the path in slash form, or the empty string for the zero
// value.
func (p ContentPath) String() string { return p.p }

// IsZero reports whether p is the zero value, which is not a valid path.
func (p ContentPath) IsZero() bool { return p.p == "" }

// Dir returns the directory part of the path, "." when it has none.
func (p ContentPath) Dir() string { return path.Dir(p.p) }

// Base returns the final segment of the path.
func (p ContentPath) Base() string { return path.Base(p.p) }

// Ext returns the extension of the final segment, including the leading dot,
// or the empty string when there is none.
func (p ContentPath) Ext() string { return path.Ext(p.p) }

// IsMarkdown reports whether the path has a Markdown extension, ".md" or
// ".markdown", compared case-insensitively.
func (p ContentPath) IsMarkdown() bool {
	ext := path.Ext(p.p)
	return strings.EqualFold(ext, ".md") || strings.EqualFold(ext, ".markdown")
}

// MarshalText implements encoding.TextMarshaler.
func (p ContentPath) MarshalText() ([]byte, error) { return []byte(p.p), nil }

// UnmarshalText implements encoding.TextUnmarshaler. It validates the text the
// same way NewContentPath does and leaves the receiver unchanged on failure.
func (p *ContentPath) UnmarshalText(b []byte) error {
	parsed, err := NewContentPath(string(b))
	if err != nil {
		return err
	}
	*p = parsed
	return nil
}

// gitDir is the directory Git keeps its metadata in. Content may not be
// written into it in any casing, whatever backend is in use.
const gitDir = ".git"

// rejectGitSegment rejects s when any of its segments is Git's metadata
// directory. Content must never be written there, and the rule costs nothing
// for a backend that has no repository.
//
// The comparison is case-insensitive. On a case-insensitive checkout, which is
// the default on macOS and Windows, ".GIT/config" and ".git/config" name the
// same file, so a case-sensitive rule would reject the attempt on Linux and
// admit it on exactly the systems where it resolves.
func rejectGitSegment(kind, s string) error {
	for _, seg := range strings.Split(s, "/") {
		if strings.EqualFold(seg, gitDir) {
			return invalidf(kind, s, "contains a %q segment, which is Git's metadata directory", seg)
		}
	}
	return nil
}

// validateRelPath applies the rules shared by every path-like value object in
// the core: ContentPath, Collection and AssetKey. kind names the value for the
// error message and maxBytes caps the whole string; the per-segment cap is
// always maxPathSegmentBytes.
func validateRelPath(kind, s string, maxBytes int) error {
	if s == "" {
		return invalidf(kind, s, "is empty")
	}
	if !utf8.ValidString(s) {
		return invalidf(kind, s, "is not valid UTF-8")
	}
	if len(s) > maxBytes {
		return invalidf(kind, s, "is %d bytes, over the %d-byte limit", len(s), maxBytes)
	}
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f:
			return invalidf(kind, s, "contains the control character %q", r)
		case r == '\\':
			return invalidf(kind, s, `contains a backslash; "/" is the only separator`)
		}
	}
	if strings.HasPrefix(s, "/") {
		return invalidf(kind, s, "is absolute; only relative paths are allowed")
	}
	if strings.HasSuffix(s, "/") {
		return invalidf(kind, s, "has a trailing slash")
	}

	for i, seg := range strings.Split(s, "/") {
		switch {
		case seg == "":
			return invalidf(kind, s, "contains an empty segment")
		case seg == ".":
			return invalidf(kind, s, `contains a "." segment`)
		case seg == "..":
			return invalidf(kind, s, `contains a ".." segment`)
		case len(seg) > maxPathSegmentBytes:
			return invalidf(kind, s, "has a segment of %d bytes, over the %d-byte limit", len(seg), maxPathSegmentBytes)
		case i == 0 && strings.Contains(seg, ":"):
			return invalidf(kind, s, "begins with a drive or scheme prefix")
		}
	}

	// Belt and braces: the rules above already imply this, so a mismatch means
	// one of them has drifted.
	if cleaned := path.Clean(s); cleaned != s {
		return invalidf(kind, s, "is not in cleaned form (%q)", cleaned)
	}
	return nil
}
