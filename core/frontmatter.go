package core

import (
	"maps"
	"slices"
	"time"
)

// FrontMatterFormat names the syntax a document's front-matter block is
// written in. Micawber does not define a front-matter schema: which keys mean
// what is the author's concern, not the CMS's.
type FrontMatterFormat string

// The front-matter syntaxes the core recognises.
const (
	// FrontMatterNone means the document has no front-matter block at all.
	FrontMatterNone FrontMatterFormat = ""
	// FrontMatterYAML is a block delimited by "---".
	FrontMatterYAML FrontMatterFormat = "yaml"
	// FrontMatterTOML is a block delimited by "+++".
	FrontMatterTOML FrontMatterFormat = "toml"
	// FrontMatterJSON is a leading JSON object.
	FrontMatterJSON FrontMatterFormat = "json"
)

// Valid reports whether f is one of the recognised formats. FrontMatterNone is
// valid: a document without front matter is a normal document.
func (f FrontMatterFormat) Valid() bool {
	switch f {
	case FrontMatterNone, FrontMatterYAML, FrontMatterTOML, FrontMatterJSON:
		return true
	}
	return false
}

// String returns the format as written, the empty string for FrontMatterNone.
func (f FrontMatterFormat) String() string { return string(f) }

// FrontMatter is a document's front-matter block, kept both verbatim and
// decoded.
//
// Raw is the authored bytes between the delimiters and Fields is what a
// decoder made of them. Holding both is what lets Micawber keep Git diffs
// small: a serializer must emit Raw unchanged while Fields still decodes equal
// to it, and re-encode only once the fields have actually been edited.
//
// The core neither decodes nor encodes: this type is the shape the parser
// fills in, and it presupposes no particular YAML, TOML or JSON library.
type FrontMatter struct {
	// Format is the syntax Raw is written in.
	Format FrontMatterFormat
	// Raw is the verbatim block, excluding the delimiter lines. It is nil when
	// there is no front matter.
	Raw []byte
	// Fields is the decoded block. It is map[string]any because that is what
	// every candidate decoder already produces.
	Fields map[string]any
}

// IsZero reports whether fm carries no front matter at all.
func (fm FrontMatter) IsZero() bool {
	return fm.Format == FrontMatterNone && len(fm.Raw) == 0 && len(fm.Fields) == 0
}

// Clone returns a copy that shares no Raw slice or Fields map with fm, so
// either may be mutated without disturbing the other. Values nested inside
// Fields are copied by reference: cloning cannot deep-copy an arbitrary any.
func (fm FrontMatter) Clone() FrontMatter {
	return FrontMatter{
		Format: fm.Format,
		Raw:    slices.Clone(fm.Raw),
		Fields: maps.Clone(fm.Fields),
	}
}

// Lookup returns the raw decoded value stored under key.
func (fm FrontMatter) Lookup(key string) (any, bool) {
	v, ok := fm.Fields[key]
	return v, ok
}

// Text returns the value under key when it is a string. It does not coerce
// across types: a bool or a number reports false rather than being formatted.
//
// It is named Text rather than String so that FrontMatter does not read as a
// fmt.Stringer.
func (fm FrontMatter) Text(key string) (string, bool) {
	s, ok := fm.Fields[key].(string)
	return s, ok
}

// Bool returns the value under key when it is a bool. The string "true" is not
// a bool.
func (fm FrontMatter) Bool(key string) (bool, bool) {
	b, ok := fm.Fields[key].(bool)
	return b, ok
}

// Time returns the value under key when it is a time.Time, or a string in one
// of the layouts front matter commonly uses: RFC 3339, "2006-01-02 15:04:05",
// or a bare "2006-01-02", which is read as midnight UTC.
func (fm FrontMatter) Time(key string) (time.Time, bool) {
	switch v := fm.Fields[key].(type) {
	case time.Time:
		return v, true
	case string:
		for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
			if t, err := time.Parse(layout, v); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

// Strings returns the value under key when it is a list of strings, whether
// the decoder produced []string or []any. A list holding anything but strings
// reports false rather than dropping the offending element. The result is a
// copy, so mutating it does not reach back into Fields.
func (fm FrontMatter) Strings(key string) ([]string, bool) {
	switch v := fm.Fields[key].(type) {
	case []string:
		return slices.Clone(v), true
	case []any:
		out := make([]string, 0, len(v))
		for _, elem := range v {
			s, ok := elem.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	}
	return nil, false
}

// Validate reports whether fm is well formed: a known format, no fields or raw
// bytes when there is no format, and no empty field key.
//
// The returned error unwraps to ErrInvalid.
func (fm FrontMatter) Validate() error {
	const kind = "front matter"

	if !fm.Format.Valid() {
		return invalidf(kind, fm.Format.String(), "is not a known format")
	}
	if fm.Format == FrontMatterNone {
		if len(fm.Fields) > 0 {
			return invalidf(kind, fm.Format.String(), "carries fields but names no format")
		}
		if len(fm.Raw) > 0 {
			return invalidf(kind, fm.Format.String(), "carries raw bytes but names no format")
		}
	}
	for key := range fm.Fields {
		if key == "" {
			return invalidf(kind, fm.Format.String(), "has a field with an empty key")
		}
	}
	return nil
}
