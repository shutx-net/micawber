package markdown

import (
	"bytes"
	"fmt"

	"github.com/shutx-net/micawber/core"
)

// MaxFrontMatterBytes is the largest a front-matter block may be, one mebibyte.
//
// It bounds the scan for the closing delimiter: without a limit, a single
// opening "---" would turn any large file into a full scan, which is a
// denial-of-service surface on content nobody has vetted. A block that reaches
// it is reported as invalid rather than read on.
//
// It is a constant rather than a package variable because a settable limit
// would be global mutable state; making it configurable is a matter for the
// configuration layer, not a default.
const MaxFrontMatterBytes = 1 << 20

// MaxFrontMatterLines is the largest number of lines a front-matter block may
// hold, one thousand.
//
// It exists because [MaxFrontMatterBytes] does not bound what a block costs to
// decode. yaml/v3 decodes a mapping into a map in time quadratic in the number
// of entries, so size is a poor proxy for work: 40 KB of repeated keys, under
// four percent of the byte limit, took 12.4 seconds to reject, and a full-size
// block of them exhausted memory instead of finishing. Counting the newlines in
// the block first is linear, runs before any decoder sees the bytes, and caps
// the worst case at a fraction of a second.
//
// A thousand lines is far more front matter than a document has any reason to
// carry, and like [MaxFrontMatterBytes] it is a constant rather than a settable
// variable, which would be global mutable state.
const MaxFrontMatterLines = 1000

// The delimiter lines that open and close a front-matter block.
const (
	yamlDelimiter = "---"
	tomlDelimiter = "+++"
)

// Parse splits data into a [Document]: the front-matter block decoded into
// [core.FrontMatter], the body verbatim, and the [Layout] of the delimiters.
//
// The returned Content has no Path and no Revision; those are the caller's, and
// a document is parsed from bytes that may not have come from a repository at
// all. FrontMatter.Raw and Content.Body alias data rather than copying it, so a
// caller that reuses its buffer should keep [core.Content.Clone] instead.
//
// A malformed front-matter block is an error matching [core.ErrInvalid]: a first
// line of "---" or "+++" means front matter and nothing else, so a block that is
// never closed, holds a NUL byte, is over [MaxFrontMatterBytes] or does not
// decode means the file is damaged. A document with no front matter is not an
// error: every byte becomes the body.
func Parse(data []byte) (Document, error) {
	p, err := scan(data)
	if err != nil {
		return Document{}, err
	}

	doc := Document{
		Content: core.Content{Body: p.body},
		Layout:  p.layout,
	}
	if p.format == core.FrontMatterNone {
		return doc, nil
	}

	cdc, ok := codecFor(p.format)
	if !ok {
		return Document{}, fmt.Errorf("markdown: %s front matter: %w", p.format, core.ErrUnsupported)
	}
	// Before the decoder, not after: this is the bound on decode cost that
	// MaxFrontMatterBytes cannot provide. See MaxFrontMatterLines.
	if lines := bytes.Count(p.raw, []byte{'\n'}); lines > MaxFrontMatterLines {
		return Document{}, invalidf(p.format, "has %d lines, over the %d-line limit", lines, MaxFrontMatterLines)
	}

	fields, err := cdc.decode(p.raw)
	if err != nil {
		return Document{}, err
	}
	doc.Content.FrontMatter = core.FrontMatter{Format: p.format, Raw: p.raw, Fields: fields}

	// Parse must not hand back a document that Bytes would then refuse to
	// serialize, and core's own rules are what decides that.
	if err := doc.Content.FrontMatter.Validate(); err != nil {
		return Document{}, err
	}
	return doc, nil
}

// parts is what scan makes of a document: the five slots of the concatenation
// identity, with the byte-order mark and the delimiter lines in layout.
//
// raw and body alias the scanned data.
type parts struct {
	layout Layout
	format core.FrontMatterFormat
	raw    []byte
	body   []byte
}

// scan splits data into its five slots without decoding anything.
//
// It knows nothing about any decoder and cannot fail for a reason a codec would
// report: it returns bytes and a format, and the only failures it has are
// structural -- a block that is never closed, one over [MaxFrontMatterBytes],
// and one holding a NUL byte. That split is what lets the JSON branch have a
// completely different shape from the delimiter branch without disturbing it.
func scan(data []byte) (parts, error) {
	noFrontMatter := parts{format: core.FrontMatterNone, body: data}

	// The mark is significant only when it immediately precedes a recognised
	// delimiter. In a document with no front matter it is simply the first bytes
	// of the body, and both branches re-emit the exact input bytes. Deciding
	// here is what keeps the two branches from each having an opinion.
	rest := data
	hasBOM := bytes.HasPrefix(rest, []byte(bom))
	if hasBOM {
		rest = rest[len(bom):]
	}

	// A leading brace is a weak signal -- ordinary text in MDX and in plenty of
	// prose -- so it must confirm itself completely, and a document that fails
	// to confirm is left entirely alone.
	if len(rest) > 0 && rest[0] == '{' {
		found, ok := scanJSON(rest, hasBOM)
		if !ok {
			return noFrontMatter, nil
		}
		return rejectNUL(found)
	}

	// A "---" or "+++" first line is a strong signal: from here, a block that
	// does not close is a damaged file to report rather than prose to present.
	openLine, blockStart := lineAt(rest, 0)
	format, delimiter, ok := openingDelimiter(openLine)
	if !ok {
		return noFrontMatter, nil
	}
	found, err := scanDelimited(rest, hasBOM, format, delimiter, blockStart)
	if err != nil {
		return parts{}, err
	}
	return rejectNUL(found)
}

// scanDelimited splits a block fenced by delimiter lines. blockStart is the
// index just past the opening line's terminator.
func scanDelimited(rest []byte, hasBOM bool, format core.FrontMatterFormat, delimiter string, blockStart int) (parts, error) {
	for at := blockStart; ; {
		switch {
		case at >= len(rest):
			return parts{}, invalidf(format, "opened on line 1 and is never closed by a %q line", delimiter)
		case at-blockStart > MaxFrontMatterBytes:
			// Report rather than read on: the closing delimiter may well be
			// there, but finding it is not worth an unbounded scan.
			return parts{}, invalidf(format, "opened on line 1 and reached the %d-byte limit with no closing %q line", MaxFrontMatterBytes, delimiter)
		}

		line, next := lineAt(rest, at)
		if !isDelimiterLine(line, delimiter) {
			at = next
			continue
		}
		return parts{
			layout: Layout{BOM: hasBOM, Open: string(rest[:blockStart]), Close: string(rest[at:next])},
			format: format,
			raw:    rest[blockStart:at],
			body:   rest[next:],
		}, nil
	}
}

// scanJSON splits a leading JSON object, and reports false when the brace it was
// given does not open one.
//
// Front matter is recognised only when the object is complete and its closing
// brace is followed by nothing but whitespace to the end of that line. Anything
// else -- a truncated object, a syntax error, trailing text -- is not front
// matter, and no error: the caller puts every byte in the body.
//
// [Layout.Open] stays empty, because JSON front matter has no opening delimiter
// line, and Layout.Close holds the blank rest of the closing line.
func scanJSON(data []byte, hasBOM bool) (parts, bool) {
	// Bound the work as the delimiter scan is bounded: an object that does not
	// complete within MaxFrontMatterBytes is treated as ordinary text rather
	// than scanned to the end of a large file.
	window := data
	if len(window) > MaxFrontMatterBytes {
		window = window[:MaxFrontMatterBytes]
	}

	decoder := jsonDecoder(window)
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		return parts{}, false
	}
	end := int(decoder.InputOffset())

	closeEnd := end
	for closeEnd < len(data) && (data[closeEnd] == ' ' || data[closeEnd] == '\t') {
		closeEnd++
	}
	switch {
	case closeEnd == len(data): // the object runs to the end of the file
	case data[closeEnd] == '\n':
		closeEnd++
	case data[closeEnd] == '\r' && closeEnd+1 < len(data) && data[closeEnd+1] == '\n':
		closeEnd += 2
	default:
		return parts{}, false
	}

	return parts{
		layout: Layout{BOM: hasBOM, Close: string(data[end:closeEnd])},
		format: core.FrontMatterJSON,
		raw:    data[:end],
		body:   data[closeEnd:],
	}, true
}

// rejectNUL is the one check the two branches share. A NUL in metadata that is
// about to be handed to a decoder, and then in later phases to a command line
// and an HTTP response, is never intentional, and refusing it here is cheaper
// than auditing every downstream consumer of Fields. The body gets the opposite
// treatment: core says it is never inspected.
func rejectNUL(p parts) (parts, error) {
	if nul := bytes.IndexByte(p.raw, 0); nul >= 0 {
		return parts{}, invalidf(p.format, "contains a NUL byte at block offset %d", nul)
	}
	return p, nil
}

// lineAt returns the line starting at i: its bytes without the terminator, and
// the index just past that terminator. A line that runs to the end of b has no
// terminator and next is len(b).
func lineAt(b []byte, i int) (line []byte, next int) {
	if end := bytes.IndexByte(b[i:], '\n'); end >= 0 {
		return b[i : i+end], i + end + 1
	}
	return b[i:], len(b)
}

// isDelimiterLine reports whether line is delimiter, ignoring a trailing CR and
// any trailing spaces and tabs.
//
// Trailing whitespace is accepted rather than rejected because [Layout] stores
// the line verbatim, so accepting it costs nothing in fidelity, while refusing
// it would drop the front matter of files every other tool reads.
func isDelimiterLine(line []byte, delimiter string) bool {
	trimmed := bytes.TrimRight(bytes.TrimSuffix(line, []byte("\r")), " \t")
	return string(trimmed) == delimiter
}

// openingDelimiter reports which format, if any, the document's first line
// opens. Only the delimiter that opened a block can close it, so the delimiter
// is returned alongside the format.
func openingDelimiter(line []byte) (format core.FrontMatterFormat, delimiter string, ok bool) {
	switch {
	case isDelimiterLine(line, yamlDelimiter):
		return core.FrontMatterYAML, yamlDelimiter, true
	case isDelimiterLine(line, tomlDelimiter):
		return core.FrontMatterTOML, tomlDelimiter, true
	}
	return core.FrontMatterNone, "", false
}
