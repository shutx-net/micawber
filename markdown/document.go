package markdown

import (
	"strings"

	"github.com/shutx-net/micawber/core"
)

// bom is the UTF-8 byte-order mark. Only this encoding's mark is recognised: a
// UTF-16 mark means the file is not UTF-8, so it is left as ordinary leading
// body bytes rather than guessed at.
const bom = "\ufeff"

// Document is a parsed Markdown file: the domain content, plus the byte-level
// shape of the front-matter delimiters it was written with.
//
// The two are separate because [core.Content] carries what the document means
// and Layout carries how its bytes were arranged. A caller that only stores or
// renders content can ignore Layout entirely; a caller that writes the document
// back should keep it, so the file it writes differs from the file it read only
// where the content does.
type Document struct {
	// Content is the document itself: its front matter and its verbatim body.
	Content core.Content
	// Layout is the shape of the delimiters. Its zero value is the canonical
	// shape for the content's format.
	Layout Layout
}

// Layout is the byte-level shape of a front-matter block's delimiters.
//
// It exists because the exact bytes of the delimiter lines are nowhere in
// [core.FrontMatter]: their line terminator, any trailing whitespace, whether
// the closing line ran to EOF, and whether a byte-order mark preceded them are
// all needed to write a file back unchanged, and none of them mean anything to
// the domain. Its fields are strings and a bool rather than byte slices, so a
// Layout is comparable, immutable and cheap to assert on.
//
// The zero value is the canonical shape: [Document.Bytes] substitutes the
// format's default delimiters for it, so a Document assembled by hand
// serializes sensibly.
type Layout struct {
	// BOM reports whether a UTF-8 byte-order mark preceded the opening
	// delimiter. It is false for a document with no front matter, where a mark
	// is simply the first bytes of the body.
	BOM bool
	// Open is the opening delimiter line, verbatim and including its
	// terminator. It is empty for JSON front matter, which has no delimiter
	// lines, and for a document with no front matter.
	Open string
	// Close is the closing delimiter line, verbatim and including its
	// terminator, which is absent when the line ran to EOF. For JSON front
	// matter it holds the whitespace and terminator between the closing brace
	// and the body.
	Close string
}

// IsZero reports whether l carries no delimiter bytes at all.
func (l Layout) IsZero() bool { return !l.BOM && l.Open == "" && l.Close == "" }

// lineEnding returns the line terminator the document's delimiters were written
// with: the terminator of Open, or of Close when Open has none, or LF.
//
// It is read from the delimiters rather than from the block because an empty
// block has no line to read a terminator from.
func (l Layout) lineEnding() string {
	for _, line := range []string{l.Open, l.Close} {
		switch {
		case strings.HasSuffix(line, "\r\n"):
			return "\r\n"
		case strings.HasSuffix(line, "\n"):
			return "\n"
		}
	}
	return "\n"
}

// defaultLayout returns the canonical delimiters for f: the format's delimiter
// lines terminated with LF.
//
// [core.FrontMatterJSON] and [core.FrontMatterNone] have no delimiter lines, so
// their canonical layout is the zero value. For JSON that is also what Parse
// produces for an object that runs to the end of the file, which is why
// Document.Bytes can substitute this for a zero Layout unconditionally.
func defaultLayout(f core.FrontMatterFormat) Layout {
	switch f {
	case core.FrontMatterYAML:
		return Layout{Open: yamlDelimiter + "\n", Close: yamlDelimiter + "\n"}
	case core.FrontMatterTOML:
		return Layout{Open: tomlDelimiter + "\n", Close: tomlDelimiter + "\n"}
	}
	return Layout{}
}
