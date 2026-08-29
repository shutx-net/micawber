package markdown

import (
	"bytes"
	"errors"
	"strconv"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"

	"github.com/shutx-net/micawber/core"
)

// tomlCodec reads and writes TOML front matter, the "+++" block Hugo uses.
//
// This is the only file that imports github.com/pelletier/go-toml/v2, and
// markdown/architecture_test.go fails if that changes. The library supplies
// decoding and encoding and nothing else. No Go TOML library reports a
// document's authored key order through an API with a compatibility guarantee,
// so the order an edited block is re-emitted in comes from tomlTopLevelKeys, a
// scanner over the raw block written below.
//
// TOML comments do not survive a re-encode. No Go TOML library round-trips
// them, and TestReencodingLosesTOMLComments asserts the loss head-on so that
// nobody discovers it in production. Neither does a string's quoting: the
// encoder writes literal strings in single quotes wherever the content allows
// and offers no option, so a double-quoted value is requoted on the block's
// first edit, which TestEditingOneTOMLFieldAlsoRequotesTheOtherStrings asserts
// just as plainly.
type tomlCodec struct{}

// decode reads a TOML table into fields. An empty block decodes to an empty map
// rather than to nil.
func (tomlCodec) decode(raw []byte) (map[string]any, error) {
	var fields map[string]any
	if err := toml.Unmarshal(normalizeLF(raw), &fields); err != nil {
		return nil, tomlInvalid(err)
	}
	return nonNilFields(fields), nil
}

// encode writes fields as a TOML table, in the authored key order where there
// was an authored block and sorted where there was not. Tables always come after
// the scalars: TOML's own syntax forbids a top-level scalar after a table
// header, so that grouping is not a preference.
//
// Comments in the authored block do not survive.
func (tomlCodec) encode(fields map[string]any, prev []byte) ([]byte, error) {
	var authored []string
	if len(prev) > 0 {
		authored = tomlTopLevelKeys(prev)
	}
	return tomlEncode(fields, tomlKeyOrder(fields, authored))
}

// tomlKeyOrder returns the keys of fields in the order a block must emit them:
// the authored order where there is one, with every table key moved after the
// last non-table key.
func tomlKeyOrder(fields map[string]any, authored []string) []string {
	scalars := make([]string, 0, len(fields))
	tables := make([]string, 0, len(fields))
	for _, key := range orderedKeys(fields, authored) {
		if isTOMLTable(fields[key]) {
			tables = append(tables, key)
		} else {
			scalars = append(scalars, key)
		}
	}
	return append(scalars, tables...)
}

// isTOMLTable reports whether value is written as a table or an array of
// tables, which is what decides whether it has to come after the scalars.
func isTOMLTable(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		return true
	case []map[string]any:
		return true
	case []any:
		return len(typed) > 0 && isTOMLTable(typed[0])
	}
	return false
}

// tomlEncode writes the keys in order, one encoder call per key.
//
// It is key by key because a TOML encoder sorts the entries of a whole map --
// measured of both libraries this package has used -- and a Go map has no
// insertion order to offer it in any case. So the loop is not a workaround for
// one library: it is how key order reaches the output at all.
func tomlEncode(fields map[string]any, order []string) ([]byte, error) {
	var out []byte
	for _, key := range order {
		value, ok := fields[key]
		if !ok {
			continue
		}
		if value == nil {
			// nil has no TOML representation, and the encoder skips a nil
			// entry silently -- measured: encoding {"a": nil} produces no
			// output and no error. So this guard is the only thing between a
			// field explicitly set to nil and its disappearance from the
			// written document, and refusing the write is better than losing
			// the edit. It is not redundant with the library.
			return nil, invalidf(core.FrontMatterTOML, "cannot represent a nil value")
		}

		var buf bytes.Buffer
		encoder := toml.NewEncoder(&buf)
		if err := encoder.Encode(map[string]any{key: value}); err != nil {
			// The library's message may quote the offending value.
			return nil, invalidf(core.FrontMatterTOML, "cannot be encoded")
		}
		out = append(out, bytes.TrimLeft(buf.Bytes(), "\n")...)
	}
	return out, nil
}

// tomlTopLevelKeys returns the names of the block's top-level keys, in the order
// they were authored. An edited block has to be re-emitted in that order, and no
// Go TOML library reports it through an API with a compatibility guarantee.
//
// This is a lexer over the raw bytes and not a parser. It knows just enough
// about comments, strings, arrays, inline tables and table headers to find where
// each logical line begins, and it reads nothing else: no values, no types, no
// validation. Its input has already been decoded successfully by the caller --
// frontMatterBlock decodes the authored block before anything reaches an encoder
// -- so there is no malformedness left here to find that the decoder has not
// already reported, and there is no error to return.
//
// That precondition is also what bounds a mistake. A key this function fails to
// report falls into orderedKeys' sorted tail, and one it reports that is not in
// fields is skipped, so the worst a scanning bug can cost is a noisier diff,
// never a wrong value or a lost field. It must still terminate and must not
// panic on any input, because a precondition is not a proof;
// FuzzTOMLTopLevelKeysMatchTheDecodedFields is what holds it to that.
func tomlTopLevelKeys(raw []byte) []string {
	b := normalizeLF(raw)

	var order []string
	seen := map[string]bool{}
	record := func(name string) {
		if !seen[name] {
			seen[name] = true
			order = append(order, name)
		}
	}

	// Past the first table header TOML has no more top-level key-value lines:
	// every key from there on belongs to a table, and only a header introduces
	// a top-level name.
	inTable := false

	// Each turn of the loop starts at a logical line, so the first byte is
	// enough to say what the line is.
	for i := 0; i < len(b); {
		switch b[i] {
		case ' ', '\t', '\r', '\n':
			i++
		case '#':
			i = tomlSkipLine(b, i)
		case '[':
			name, next := tomlHeaderName(b, i)
			record(name)
			inTable = true
			i = next
		default:
			name, next, ok := tomlDottedKey(b, i)
			if !ok {
				// Not a key-value line, which well-formed TOML cannot put
				// here. Drop the line and carry on: this function's job is
				// to terminate, not to diagnose.
				i = tomlSkipLine(b, i)
				continue
			}
			if !inTable {
				record(name)
			}
			i = tomlSkipValue(b, next)
		}
	}
	return order
}

// tomlSkipLine returns the index just past the next line terminator at or after
// i, or len(b) when the last line of the block has none.
func tomlSkipLine(b []byte, i int) int {
	for ; i < len(b); i++ {
		if b[i] == '\n' {
			return i + 1
		}
	}
	return i
}

// tomlKeyPart reads the single key part at i and returns its name and the index
// just past it. A part is bare, "quoted" or 'literal'.
//
// The quoted form is unescaped because the name the decoder puts in the map is
// the unescaped one, and the two have to agree for the key sets to compare
// equal.
func tomlKeyPart(b []byte, i int) (string, int) {
	if i >= len(b) {
		return "", i
	}
	switch b[i] {
	case '"':
		var name []byte
		j := i + 1
		for j < len(b) && b[j] != '"' && b[j] != '\n' {
			if b[j] != '\\' {
				name = append(name, b[j])
				j++
				continue
			}
			name, j = tomlEscape(name, b, j)
		}
		if j < len(b) && b[j] == '"' {
			j++
		}
		return string(name), j
	case '\'':
		j := i + 1
		for j < len(b) && b[j] != '\'' && b[j] != '\n' {
			j++
		}
		name := string(b[i+1 : j])
		if j < len(b) && b[j] == '\'' {
			j++
		}
		return name, j
	default:
		j := i
		for j < len(b) && (b[j] == '-' || b[j] == '_' ||
			b[j] >= '0' && b[j] <= '9' ||
			b[j] >= 'a' && b[j] <= 'z' ||
			b[j] >= 'A' && b[j] <= 'Z') {
			j++
		}
		return string(b[i:j]), j
	}
}

// tomlEscape appends the escape sequence at the backslash at i to dst, and
// returns dst and the index just past the sequence.
//
// A sequence TOML does not define is appended as written rather than reported: a
// block holding one has already failed to decode, and this function says what a
// key is called, not whether it is legal.
func tomlEscape(dst, b []byte, i int) ([]byte, int) {
	if i+1 >= len(b) {
		return append(dst, b[i:]...), len(b)
	}

	var digits int
	switch b[i+1] {
	case 'b':
		return append(dst, '\b'), i + 2
	case 't':
		return append(dst, '\t'), i + 2
	case 'n':
		return append(dst, '\n'), i + 2
	case 'f':
		return append(dst, '\f'), i + 2
	case 'r':
		return append(dst, '\r'), i + 2
	case 'e':
		return append(dst, 0x1b), i + 2
	case '"':
		return append(dst, '"'), i + 2
	case '\\':
		return append(dst, '\\'), i + 2
	case 'x':
		digits = 2
	case 'u':
		digits = 4
	case 'U':
		digits = 8
	default:
		return append(dst, b[i:i+2]...), i + 2
	}

	end := i + 2 + digits
	if end > len(b) {
		return append(dst, b[i:]...), len(b)
	}
	value, err := strconv.ParseUint(string(b[i+2:end]), 16, 32)
	if err != nil || value > utf8.MaxRune {
		return append(dst, b[i:end]...), end
	}
	return utf8.AppendRune(dst, rune(value)), end
}

// tomlDottedKey reads the key of the key-value line at i and returns its first
// part -- the only one that names a top-level key -- and the index just past the
// "=" that follows it.
//
// It reports false for a line that is not a key-value line, which well-formed
// TOML does not put where this is called.
func tomlDottedKey(b []byte, i int) (string, int, bool) {
	first, j := tomlKeyPart(b, i)
	if j == i {
		return "", i, false
	}

	for j < len(b) {
		switch b[j] {
		case ' ', '\t':
			j++
		case '.':
			j++
			for j < len(b) && (b[j] == ' ' || b[j] == '\t') {
				j++
			}
			_, next := tomlKeyPart(b, j)
			if next == j {
				return "", i, false
			}
			j = next
		case '=':
			return first, j + 1, true
		default:
			return "", i, false
		}
	}
	return "", i, false
}

// tomlHeaderName reads the table header at i -- "[a.b]" or "[[a.b]]" -- and
// returns the top-level name it introduces, which is its first key part, and the
// index of the line after it. A header is alone on its line but for a comment.
func tomlHeaderName(b []byte, i int) (string, int) {
	j := i + 1
	if j < len(b) && b[j] == '[' {
		j++ // an array of tables
	}
	for j < len(b) && (b[j] == ' ' || b[j] == '\t') {
		j++
	}

	name, j := tomlKeyPart(b, j)
	return name, tomlSkipLine(b, j)
}

// tomlSkipValue advances past the value at i and returns the index of the next
// logical line.
//
// Bracket and brace depth is carried across newlines rather than reset at each
// one, because an array or an inline table can span them: a line reading
// "[1, 2]," inside a multi-line array is not a table header, and depth is the
// only thing that tells them apart.
func tomlSkipValue(b []byte, i int) int {
	depth := 0
	for i < len(b) {
		switch b[i] {
		case '\n':
			i++
			if depth == 0 {
				return i
			}
		case '#':
			i = tomlSkipLine(b, i)
			if depth == 0 {
				return i
			}
		case '"', '\'':
			i = tomlSkipString(b, i)
		case '[', '{':
			depth++
			i++
		case ']', '}':
			if depth > 0 {
				depth--
			}
			i++
		default:
			i++
		}
	}
	return i
}

// tomlSkipString advances past the string whose opening quote is at i.
//
// A basic string's backslash escapes the byte after it and a literal string's
// does not, which is why 'C:\' ends at its quote.
func tomlSkipString(b []byte, i int) int {
	quote := b[i]
	if i+2 < len(b) && b[i+1] == quote && b[i+2] == quote {
		return tomlSkipMultilineString(b, i+3, quote)
	}

	for j := i + 1; j < len(b) && b[j] != '\n'; j++ {
		if b[j] == quote {
			return j + 1
		}
		if quote == '"' && b[j] == '\\' && j+1 < len(b) {
			j++
		}
	}
	return tomlSkipLine(b, i)
}

// tomlSkipMultilineString advances past the body of a multi-line string that
// opened before i, and returns the index just past its closing delimiter.
func tomlSkipMultilineString(b []byte, i int, quote byte) int {
	for i < len(b) {
		if quote == '"' && b[i] == '\\' {
			i = min(i+2, len(b))
			continue
		}
		if b[i] != quote {
			i++
			continue
		}

		run := i
		for run < len(b) && b[run] == quote {
			run++
		}
		if run-i < 3 {
			// One or two quotes belong to the content anywhere inside.
			i = run
			continue
		}
		// Three close the string, and up to two more before them belong to
		// the content, so the delimiter is the last three of a run of three,
		// four or five.
		return min(run, i+5)
	}
	return i
}

// tomlInvalid wraps a TOML failure as a [core.ErrInvalid].
//
// As with YAML the library's own message is dropped rather than wrapped, and
// here the rule is stricter than it looks. A toml.DecodeError offers a String
// that renders the offending line together with every line before it -- which
// for a front-matter block is exactly where a credential would be -- and its
// Error is no safer, naming a key out of the document for a duplicate. So the
// row that Position reports is the only thing read from it, String is never
// called, and TestParseRejectsAMalformedTOMLBlockWithoutEchoingIt is what stops
// that being quietly undone.
func tomlInvalid(err error) error {
	// A *toml.DecodeError, so errors.As can succeed with a nil pointer and the
	// nil check has to come before Position is called.
	var decodeErr *toml.DecodeError
	if errors.As(err, &decodeErr) && decodeErr != nil {
		if row, _ := decodeErr.Position(); row > 0 {
			return invalidf(core.FrontMatterTOML, "is not well formed at block line %d", row)
		}
	}
	if line, ok := errorLine(err); ok {
		return invalidf(core.FrontMatterTOML, "is not well formed at block line %d", line)
	}
	return invalidf(core.FrontMatterTOML, "is not well formed")
}
