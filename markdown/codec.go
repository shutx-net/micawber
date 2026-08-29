package markdown

import (
	"bytes"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/shutx-net/micawber/core"
)

// codec is the seam between this package and a format-specific library. It is
// unexported and not registrable on purpose: [core.FrontMatterFormat] is a
// closed set, so this is a seam for three known implementations rather than an
// extension point.
//
// decode and encode are one interface rather than two switches because the two
// halves of a format have to agree about that format's conventions for a
// document to round-trip at all. It also keeps each third-party import confined
// to one file, which markdown/architecture_test.go can then police.
type codec interface {
	// decode reads a front-matter block into fields. The block is the verbatim
	// authored bytes, so an implementation must tolerate CRLF line endings, and
	// it returns a non-nil map for a block that holds nothing.
	decode(raw []byte) (map[string]any, error)

	// encode writes fields as a block, with LF line terminators.
	//
	// prev is the previously authored block, or empty for a document that never
	// had one. An implementation uses it to keep everything the edit did not
	// touch: the authored key order, and for YAML the comments and scalar
	// styles too. With no prev the output is canonical, keys sorted, because
	// there is no authored order to honour.
	encode(fields map[string]any, prev []byte) ([]byte, error)
}

// codecFor returns the codec for f. It reports false for
// [core.FrontMatterNone], which needs no codec, and for a format core does not
// recognise.
func codecFor(f core.FrontMatterFormat) (codec, bool) {
	switch f {
	case core.FrontMatterYAML:
		return yamlCodec{}, true
	case core.FrontMatterTOML:
		return tomlCodec{}, true
	case core.FrontMatterJSON:
		return jsonCodec{}, true
	}
	return nil, false
}

// normalizeLF returns a fresh copy of b with CRLF line endings converted to LF.
//
// Decoders see this copy and never the authored bytes, so no format library's
// treatment of a stray CR can affect what a document is decoded to mean, and a
// CRLF document decodes to the same fields as its LF twin.
func normalizeLF(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
}

// nonNilFields returns fields, or an empty map when the block held nothing.
//
// Every codec returns a non-nil map so that an empty block stays
// distinguishable from no block at all: the first is front matter the author
// wrote and left empty, the second is a document that never had any.
func nonNilFields(fields map[string]any) map[string]any {
	if fields == nil {
		return map[string]any{}
	}
	return fields
}

// fieldsEqual reports whether two field maps say the same thing, and is what
// makes the serializer's passthrough safe rather than a guess: a document whose
// fields still match its authored block is emitted byte for byte.
//
// A nil map and an empty map are equal, because an empty block decodes to an
// empty map while a hand-built Content usually has nil. The comparison is
// otherwise conservative in the right direction: anything it cannot prove equal
// merely causes a re-encode, never a lost edit.
func fieldsEqual(a, b map[string]any) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return valuesEqual(a, b)
}

// valuesEqual is reflect.DeepEqual with one exception: two NaN values are equal.
//
// DeepEqual compares floats with ==, which reports NaN != NaN, so a block
// holding a NaN would never compare equal to the fields decoded from it and an
// unedited document would be re-encoded on every write. The containers the three
// decoders produce are walked so that a nested NaN is caught too.
func valuesEqual(a, b any) bool {
	switch left := a.(type) {
	case float64:
		right, ok := b.(float64)
		return ok && (left == right || math.IsNaN(left) && math.IsNaN(right))
	case map[string]any:
		right, ok := b.(map[string]any)
		if !ok || len(left) != len(right) {
			return false
		}
		for key, value := range left {
			other, ok := right[key]
			if !ok || !valuesEqual(value, other) {
				return false
			}
		}
		return true
	case []any:
		right, ok := b.([]any)
		if !ok || len(left) != len(right) {
			return false
		}
		for i := range left {
			if !valuesEqual(left[i], right[i]) {
				return false
			}
		}
		return true
	case []map[string]any: // never from a decoder; a hand-built Content can hold one
		right, ok := b.([]map[string]any)
		if !ok || len(left) != len(right) {
			return false
		}
		for i := range left {
			if !valuesEqual(left[i], right[i]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(a, b)
}

// orderedKeys returns the keys of fields in the order a re-encoded block should
// emit them: the authored keys that survive, in their authored order, then
// whatever is left sorted.
//
// New keys are sorted rather than appended in map order because a map has no
// order and a write must be deterministic. This is the same rule in all three
// encoders, so there is exactly one copy of it.
func orderedKeys(fields map[string]any, authored []string) []string {
	keys := make([]string, 0, len(fields))
	seen := make(map[string]bool, len(fields))

	for _, key := range authored {
		if _, ok := fields[key]; ok && !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}

	rest := make([]string, 0, len(fields)-len(keys))
	for key := range fields {
		if !seen[key] {
			rest = append(rest, key)
		}
	}
	slices.Sort(rest)
	return append(keys, rest...)
}

// invalidf reports a malformed document as a [core.ValidationError], which
// unwraps to [core.ErrInvalid].
//
// Value is the format name and never a byte of the document: a front-matter
// block is exactly where a user may have put an API token, and
// core.ValidationError documents its Value as safe to log and to return to an
// API client.
func invalidf(f core.FrontMatterFormat, reason string, args ...any) error {
	return &core.ValidationError{
		Kind:   "front matter",
		Value:  f.String(),
		Reason: fmt.Sprintf(reason, args...),
	}
}

// errorLine returns the line number a decoder's error names, if any.
//
// It reads the number out of the library's message and nothing else: the
// message itself may quote an anchor name, a duplicate key or another token
// from the block, so it never reaches the error this package returns, while the
// line number is what makes such an error addressable.
func errorLine(err error) (int, bool) {
	const marker = "line "

	message := err.Error()
	at := strings.Index(message, marker)
	if at < 0 {
		return 0, false
	}

	digits := message[at+len(marker):]
	end := 0
	for end < len(digits) && digits[end] >= '0' && digits[end] <= '9' {
		end++
	}
	line, convErr := strconv.Atoi(digits[:end])
	if convErr != nil || line <= 0 {
		return 0, false
	}
	return line, true
}
