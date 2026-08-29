package markdown

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/shutx-net/micawber/core"
)

// jsonCodec reads and writes JSON front matter: a leading object, with no
// delimiter lines around it. It uses the standard library only, which has the
// useful side effect that this package's own framework can be exercised without
// touching either format dependency.
//
// Numbers decode to json.Number, so a number from JSON front matter has a
// different Go type from YAML's int and TOML's int64. That is deliberate: an
// integer wider than a float64 or a high-precision decimal must be re-emitted
// with the digits the author wrote, and core's accessors do not cover numbers,
// so callers already type-switch.
type jsonCodec struct{}

// decode reads the object into fields. The block is the whole object text,
// braces included, so it decodes on its own.
func (jsonCodec) decode(raw []byte) (map[string]any, error) {
	var fields map[string]any
	if err := jsonDecoder(raw).Decode(&fields); err != nil {
		return nil, jsonInvalid(raw, err)
	}
	return nonNilFields(fields), nil
}

// encode writes fields as a JSON object, in the authored key order and at the
// authored indentation where there was an authored block, and sorted at two
// spaces where there was not.
func (jsonCodec) encode(fields map[string]any, prev []byte) ([]byte, error) {
	var authored []string
	if len(prev) > 0 {
		keys, err := jsonTopLevelKeys(prev)
		if err != nil {
			return nil, err
		}
		authored = keys
	}
	return jsonEncode(fields, orderedKeys(fields, authored), jsonIndent(prev))
}

// jsonIndent returns the indentation prev writes its keys at, or two spaces.
//
// An object authored on a single line has no key line to read, so it becomes a
// multi-line object on the first edit; every later write then matches it.
func jsonIndent(prev []byte) string {
	_, afterBrace, found := bytes.Cut(normalizeLF(prev), []byte("\n"))
	if !found {
		return "  "
	}
	width := 0
	for width < len(afterBrace) && (afterBrace[width] == ' ' || afterBrace[width] == '\t') {
		width++
	}
	if width == 0 {
		return "  "
	}
	return string(afterBrace[:width])
}

// jsonEncode writes an object with the keys in order, at the given indentation.
//
// Each value is marshalled on its own so that a json.Number is written as the
// digits it was read with, rather than through a float64.
func jsonEncode(fields map[string]any, order []string, indent string) ([]byte, error) {
	out := []byte("{\n")
	for i, key := range order {
		name, err := jsonMarshal(key, indent)
		if err != nil {
			return nil, invalidf(core.FrontMatterJSON, "cannot be encoded")
		}
		value, err := jsonMarshal(fields[key], indent)
		if err != nil {
			return nil, invalidf(core.FrontMatterJSON, "cannot be encoded")
		}

		out = append(out, indent...)
		out = append(out, name...)
		out = append(out, ": "...)
		out = append(out, value...)
		if i < len(order)-1 {
			out = append(out, ',')
		}
		out = append(out, '\n')
	}
	return append(out, '}'), nil
}

// jsonMarshal writes one value, indented for a line that is already one level
// deep.
//
// It goes through an encoder rather than json.Marshal to turn off HTML
// escaping: a URL holding an ampersand must not come back as \u0026, which
// would be a diff the author did not ask for.
func jsonMarshal(value any, indent string) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent(indent, indent)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// jsonDecoder returns a decoder over b that keeps every number as the text it
// was written with.
func jsonDecoder(b []byte) *json.Decoder {
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	return decoder
}

// jsonTopLevelKeys returns the object's keys in the order they were authored, so
// that an edited block can be re-emitted without reordering it.
//
// It walks tokens rather than decoding into a map because a map has no order to
// report. A key repeated in the source is reported once, at its first position.
func jsonTopLevelKeys(raw []byte) ([]string, error) {
	decoder := jsonDecoder(raw)

	opening, err := decoder.Token()
	if err != nil {
		return nil, jsonInvalid(raw, err)
	}
	if brace, ok := opening.(json.Delim); !ok || brace != '{' {
		return nil, invalidf(core.FrontMatterJSON, "is not an object")
	}

	var keys []string
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, jsonInvalid(raw, err)
		}
		key, ok := token.(string)
		if !ok {
			return nil, invalidf(core.FrontMatterJSON, "has an object key that is not a string")
		}
		// Decode consumes the whole value, however deeply nested, which leaves
		// the decoder on the next key.
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, jsonInvalid(raw, err)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
	}
	return keys, nil
}

// jsonInvalid wraps an encoding/json failure as a [core.ErrInvalid].
//
// The library's message is dropped for the same reason as YAML's and TOML's: it
// can quote the offending token. A syntax error carries a byte offset rather
// than a line, so the line is counted here.
func jsonInvalid(raw []byte, err error) error {
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return invalidf(core.FrontMatterJSON, "is not well formed at block line %d", lineAtOffset(raw, syntaxErr.Offset))
	}
	return invalidf(core.FrontMatterJSON, "is not well formed")
}

// lineAtOffset returns the 1-based number of the line holding the byte at
// offset.
func lineAtOffset(b []byte, offset int64) int {
	if offset > int64(len(b)) {
		offset = int64(len(b))
	}
	if offset < 0 {
		offset = 0
	}
	return 1 + bytes.Count(b[:offset], []byte("\n"))
}
