// Package markdown converts a Markdown file's bytes to and from
// [core.Content]: it detects and splits the front-matter block, decodes it into
// [core.FrontMatter], keeps the body verbatim, and serializes the whole
// document back.
//
// The contract is byte-level. A document parsed and written back unedited comes
// out byte-identical, and a document with one edited field produces a diff
// limited to that field. Git is Micawber's content database, so a parser that
// reformats what it reads would turn every save into a noisy commit.
//
// # The concatenation identity
//
// Parse assigns every input byte to exactly one of five ordered slots, and
// serialization is their concatenation in that order:
//
//	Layout.BOM  Layout.Open  FrontMatter.Raw  Layout.Close  Content.Body
//
// Fidelity therefore holds by construction rather than by accumulating special
// cases. [Layout] is the byte-level shape of the delimiters -- their exact
// terminator, any trailing whitespace, whether the closing line ran to EOF, and
// whether a byte-order mark preceded them. It lives here rather than in core
// because it is a serialization artefact with no domain meaning: core says what
// a document means, this package says how its bytes are arranged. Its zero value
// is the canonical shape, so a Document assembled by hand still serializes to a
// document that parses back.
//
// # Detection
//
// In order: a leading UTF-8 byte-order mark is stripped for detection purposes
// only; if the next byte is "{", the JSON rule below applies; otherwise the
// first line, less a trailing CR and any trailing spaces and tabs, is matched
// against "---" for YAML and "+++" for TOML; otherwise the document has no front
// matter. A leading blank line before the delimiter is not accepted, matching
// every other tool in the ecosystem. The closing delimiter is the first
// subsequent line equal to the same delimiter under the same trimming, so a
// "---" line inside the body is ordinary body text, and only the opening
// delimiter closes a block: a YAML block is not closed by an ellipsis line.
//
// The strength of the signal decides the strictness of the failure, so the two
// rules are deliberately asymmetric:
//
//   - A "---" or "+++" first line means front matter and nothing else. A block
//     that is never closed, holds a NUL byte, exceeds [MaxFrontMatterBytes] or
//     does not decode is a damaged file -- typically a bad merge -- and Parse
//     fails with an error matching [core.ErrInvalid] rather than presenting the
//     block as prose in an editor a user can save over.
//   - A leading "{" means very little; it is ordinary text in MDX and in plenty
//     of prose. It is read as front matter only when it parses as one complete
//     JSON object whose closing brace is followed by nothing but whitespace to
//     the end of that line. Anything else -- a truncated object, a syntax error,
//     trailing text on the closing line -- means the document simply has no
//     front matter, and every byte becomes the body.
//
// Both outcomes are byte-safe: the degrade path puts the whole input in the
// body, so the round trip is still exact.
//
// # Serialization
//
// [Document.Bytes] chooses between three ways of emitting the block:
//
//   - The fields still say what the authored block says: emit
//     FrontMatter.Raw verbatim, byte for byte. This is the common CMS write, a
//     body edit with untouched metadata, and it must not move a byte.
//   - The fields have been edited and there is an authored block: re-encode,
//     seeded with that block, so that entries the edit did not touch keep their
//     order, and in YAML their comments and quoting too. Key order is
//     recovered from Raw at serialization time rather than stored anywhere,
//     because Raw is the authored block and is therefore the authoritative
//     record of order.
//   - There is no authored block: encode canonically, keys sorted, since there
//     is no authored order to honour.
//
// A document written with CRLF line endings keeps them: decoding reads an
// LF-normalized copy of the block, Raw itself stays verbatim, and a re-encoded
// block is converted back to the document's own terminator.
//
// # Guarantees and limits
//
// MaxFrontMatterBytes bounds the scan for the closing delimiter: without it a
// single opening "---" would turn any large file into a full scan.
// MaxFrontMatterLines bounds what the block then costs to decode, which size
// alone does not: yaml/v3's decode is quadratic in the number of entries, so a
// block well inside the byte limit could otherwise take minutes. Invalid UTF-8
// and NUL bytes in the body are passed through untouched and never inspected --
// core already says the body is the author's business.
//
// A re-encode -- the path taken only when a field actually changed -- keeps the
// order of the entries the edit did not touch, and in YAML their comments and
// quoting too, but it is an encoder's output, not a patch to the authored bytes.
// So it does not preserve, on lines it did not otherwise change: whitespace
// between a key's colon and its value beyond a single space, blank lines between
// entries, a list authored at its parent's indentation, or a long value's
// original line breaks, which yaml/v3's emitter folds near the eightieth column.
// A single-line JSON object likewise becomes multi-line on its first edit. In a
// TOML block a string is re-emitted as a literal string in single quotes
// wherever its content allows one -- no single quote, no newline, no control
// character -- because the TOML encoder writes them that way and offers no
// option, so a double-quoted value, which is what Hugo writes, is requoted on
// the block's first edit; the change is confined to the quote characters and
// never reaches the value, and it happens once per document, since a re-encoded
// block is already in the style the encoder emits. Preserving those would mean
// patching the authored block line by line rather than encoding, which is a
// larger design than this phase justifies. The passthrough path -- every write
// that does not change a field -- moves no byte at all.
//
// Parse does not copy: FrontMatter.Raw and Content.Body alias the slice handed
// to it, as bufio.Scanner.Bytes and bytes.Split do. This package retains
// nothing, so a caller that reuses its buffer should hold [core.Content.Clone]
// instead of the value Parse returned.
//
// Errors never embed a byte of the document. A front-matter block may hold an
// API token, and [core.ValidationError] documents its Value as safe to log and
// to return to an API client, so only a format name is ever echoed.
//
// This package renders nothing: Micawber is not a static site generator, so the
// body is opaque bytes and no Markdown-to-HTML step will be added here. It
// performs no I/O of any kind -- no file, no socket, no subprocess -- and takes
// no context.Context, because there is nothing to cancel.
package markdown
