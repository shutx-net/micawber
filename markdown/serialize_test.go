package markdown

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/shutx-net/micawber/core"
)

// awkward is one document per format, each written the way an author writes and an
// encoder does not: keys out of alphabetical order, and for YAML a comment, a
// single-quoted value and an unquoted date. A serializer that re-encodes when it did
// not need to shows up as a diff here.
var awkward = map[core.FrontMatterFormat]string{
	core.FrontMatterYAML: "---\n# what this is\nzebra: 1\ntitle: 'Hello' # the title\ndate: 2024-01-02\nauthor:\n  name: Ada\ntags:\n  - go\n  - cms\napple: 2\n---\n# Body\n",
	core.FrontMatterTOML: "+++\nzebra = 1\ntitle = \"Hello\"\napple = 2\n+++\n# Body\n",
	core.FrontMatterJSON: "{\n  \"zebra\": 1,\n  \"title\": \"Hello\",\n  \"apple\": 2\n}\n# Body\n",
}

// emittedKeys returns the top-level keys of a document's front-matter block in the
// order they appear in it, using each format's own reader.
func emittedKeys(t *testing.T, format core.FrontMatterFormat, raw []byte) []string {
	t.Helper()

	switch format {
	case core.FrontMatterYAML:
		mapping, err := yamlMapping(raw)
		if err != nil {
			t.Fatalf("yamlMapping: %v", err)
		}
		var keys []string
		for i := 0; mapping != nil && i+1 < len(mapping.Content); i += 2 {
			keys = append(keys, mapping.Content[i].Value)
		}
		return keys
	case core.FrontMatterTOML:
		return tomlTopLevelKeys(raw)
	case core.FrontMatterJSON:
		order, err := jsonTopLevelKeys(raw)
		if err != nil {
			t.Fatalf("jsonTopLevelKeys: %v", err)
		}
		return order
	}
	t.Fatalf("no key reader for format %q", format)
	return nil
}

func TestBytesEmitsTheAuthoredBlockWhenFieldsAreUnchanged(t *testing.T) {
	for format, doc := range awkward {
		t.Run(format.String(), func(t *testing.T) {
			in := []byte(doc)
			parsed, err := Parse(in)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			out, err := parsed.Bytes()
			if err != nil {
				t.Fatalf("Bytes: %v", err)
			}
			if !bytes.Equal(out, in) {
				t.Errorf("an unedited document was re-encoded: %s", byteDiff(in, out))
			}
		})
	}
}

func TestBytesReencodesWhenAFieldChanges(t *testing.T) {
	for format, doc := range awkward {
		t.Run(format.String(), func(t *testing.T) {
			in := []byte(doc)
			parsed, err := Parse(in)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			parsed.Content.FrontMatter.Fields["title"] = "Goodbye"

			out, err := parsed.Bytes()
			if err != nil {
				t.Fatalf("Bytes: %v", err)
			}
			if bytes.Equal(out, in) {
				t.Fatalf("the edit was not written: the output is the authored block")
			}

			reparsed, err := Parse(out)
			if err != nil {
				t.Fatalf("Parse of the re-encoded document: %v", err)
			}
			if reparsed.Content.FrontMatter.Format != format {
				t.Errorf("Format = %q; want %q", reparsed.Content.FrontMatter.Format, format)
			}
			if got, ok := reparsed.Content.FrontMatter.Text("title"); !ok || got != "Goodbye" {
				t.Errorf(`Text("title") = (%q, %t); want ("Goodbye", true)`, got, ok)
			}
			if !bytes.Equal(reparsed.Content.Body, parsed.Content.Body) {
				t.Errorf("the body changed: %s", byteDiff(parsed.Content.Body, reparsed.Content.Body))
			}
		})
	}
}

func TestBytesReencodedOutputReparsesToTheEditedFields(t *testing.T) {
	edits := map[string]func(map[string]any){
		"changed": func(fields map[string]any) { fields["title"] = "Goodbye" },
		"added":   func(fields map[string]any) { fields["draft"] = true },
		"removed": func(fields map[string]any) { delete(fields, "apple") },
	}

	for format, doc := range awkward {
		for name, edit := range edits {
			t.Run(format.String()+"/"+name, func(t *testing.T) {
				parsed, err := Parse([]byte(doc))
				if err != nil {
					t.Fatalf("Parse: %v", err)
				}
				edit(parsed.Content.FrontMatter.Fields)
				want := copyFields(parsed.Content.FrontMatter.Fields)

				out, err := parsed.Bytes()
				if err != nil {
					t.Fatalf("Bytes: %v", err)
				}
				reparsed, err := Parse(out)
				if err != nil {
					t.Fatalf("Parse of the re-encoded document: %v", err)
				}
				if !reflect.DeepEqual(reparsed.Content.FrontMatter.Fields, want) {
					t.Errorf("re-parsed fields = %#v; want %#v", reparsed.Content.FrontMatter.Fields, want)
				}
			})
		}
	}
}

// copyFields copies fields so that a later mutation cannot rewrite what a test
// asserted against.
func copyFields(fields map[string]any) map[string]any {
	out := make(map[string]any, len(fields))
	for key, value := range fields {
		out[key] = value
	}
	return out
}

// TestBytesKeepsTheDocumentLineEndingWhenReencoding covers the CRLF case that a
// Windows checkout with core.autocrlf produces: rewriting those endings to LF would
// change every line of the file, which is the largest possible noisy diff.
func TestBytesKeepsTheDocumentLineEndingWhenReencoding(t *testing.T) {
	for _, tc := range []struct {
		format core.FrontMatterFormat
		doc    string
	}{
		{core.FrontMatterYAML, "---\r\ntitle: Hello\r\nzebra: 1\r\n---\r\n# Body\r\nMore.\r\n"},
		{core.FrontMatterTOML, "+++\r\ntitle = \"Hello\"\r\nzebra = 1\r\n+++\r\n# Body\r\nMore.\r\n"},
		{core.FrontMatterJSON, "{\r\n  \"title\": \"Hello\",\r\n  \"zebra\": 1\r\n}\r\n# Body\r\nMore.\r\n"},
	} {
		t.Run(tc.format.String(), func(t *testing.T) {
			parsed, err := Parse([]byte(tc.doc))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			parsed.Content.FrontMatter.Fields["title"] = "Goodbye"

			out, err := parsed.Bytes()
			if err != nil {
				t.Fatalf("Bytes: %v", err)
			}
			reparsed, err := Parse(out)
			if err != nil {
				t.Fatalf("Parse of the re-encoded document: %v", err)
			}

			// Every terminator in the emitted block must be a CRLF, and the body
			// must not have been touched at all.
			raw := reparsed.Content.FrontMatter.Raw
			for i, b := range raw {
				if b == '\n' && (i == 0 || raw[i-1] != '\r') {
					t.Errorf("the re-encoded block has a bare LF at block offset %d", i)
					break
				}
			}
			if got := reparsed.Layout.lineEnding(); got != "\r\n" {
				t.Errorf("lineEnding() = %q; want %q", got, "\r\n")
			}
			if !bytes.Equal(reparsed.Content.Body, parsed.Content.Body) {
				t.Errorf("the body changed: %s", byteDiff(parsed.Content.Body, reparsed.Content.Body))
			}
		})
	}
}

func TestFormatEncodesADocumentThatHasNoAuthoredBlock(t *testing.T) {
	for _, tc := range []struct {
		format    core.FrontMatterFormat
		wantOpen  string
		wantClose string
	}{
		{core.FrontMatterYAML, "---\n", "---\n"},
		{core.FrontMatterTOML, "+++\n", "+++\n"},
		{core.FrontMatterJSON, "", "\n"},
	} {
		t.Run(tc.format.String(), func(t *testing.T) {
			fields := map[string]any{"zebra": "z", "apple": "a", "mango": "m"}
			content := core.Content{
				FrontMatter: core.FrontMatter{Format: tc.format, Fields: fields},
				Body:        []byte("# Body\n"),
			}

			out, err := Format(content)
			if err != nil {
				t.Fatalf("Format: %v", err)
			}

			reparsed, err := Parse(out)
			if err != nil {
				t.Fatalf("Parse of the canonical output: %v", err)
			}
			if reparsed.Content.FrontMatter.Format != tc.format {
				t.Fatalf("Format = %q; want %q", reparsed.Content.FrontMatter.Format, tc.format)
			}
			if reparsed.Layout.Open != tc.wantOpen {
				t.Errorf("Layout.Open = %q; want %q", reparsed.Layout.Open, tc.wantOpen)
			}
			if reparsed.Layout.Close != tc.wantClose {
				t.Errorf("Layout.Close = %q; want %q", reparsed.Layout.Close, tc.wantClose)
			}
			if !reflect.DeepEqual(reparsed.Content.FrontMatter.Fields, fields) {
				t.Errorf("re-parsed fields = %#v; want %#v", reparsed.Content.FrontMatter.Fields, fields)
			}
			if !bytes.Equal(reparsed.Content.Body, content.Body) {
				t.Errorf("the body changed: %s", byteDiff(content.Body, reparsed.Content.Body))
			}

			// With no authored block there is no order to honour, so the keys are
			// sorted: a canonical output has to be deterministic.
			keys := emittedKeys(t, tc.format, reparsed.Content.FrontMatter.Raw)
			if want := []string{"apple", "mango", "zebra"}; !slices.Equal(keys, want) {
				t.Errorf("emitted keys = %q; want %q", keys, want)
			}
		})
	}
}

// TestFormatRejectsABodyThatWouldBeReadAsFrontMatter guards the worst failure this
// package could have: writing a file that means something other than the Content it
// came from, discovered as data loss one commit later. Parse can never produce such a
// Content, but a caller assembling one by hand can.
func TestFormatRejectsABodyThatWouldBeReadAsFrontMatter(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"a yaml block", "---\ntitle: Hello\n---\nprose\n"},
		{"a toml block", "+++\ntitle = \"Hello\"\n+++\nprose\n"},
		{"a json object", "{\"title\": \"Hello\"}\nprose\n"},
		{"an unterminated yaml block", "---\ntitle: Hello\nprose\n"},
		{"a mark then a yaml block", "\ufeff---\ntitle: Hello\n---\nprose\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Format(core.Content{Body: []byte(tc.body)})
			if !errors.Is(err, core.ErrInvalid) {
				t.Errorf("Format error = %v; want one matching core.ErrInvalid", err)
			}
		})
	}

	// A body that merely contains a delimiter line further down is fine, and so is
	// one that opens with a brace that is not an object.
	for _, body := range []string{"prose\n\n---\n\nmore\n", "{ not an object\nprose\n"} {
		if _, err := Format(core.Content{Body: []byte(body)}); err != nil {
			t.Errorf("Format(%q) = %v; want no error", body, err)
		}
	}
}

func TestBytesRejectsInvalidFrontMatter(t *testing.T) {
	for _, tc := range []struct {
		name string
		fm   core.FrontMatter
	}{
		{"fields without a format", core.FrontMatter{Fields: map[string]any{"title": "Hello"}}},
		{"raw without a format", core.FrontMatter{Raw: []byte("title: Hello\n")}},
		{"an unknown format", core.FrontMatter{Format: core.FrontMatterFormat("xml")}},
		{"an empty field key", core.FrontMatter{Format: core.FrontMatterYAML, Fields: map[string]any{"": "Hello"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Document{Content: core.Content{FrontMatter: tc.fm}}.Bytes()
			if !errors.Is(err, core.ErrInvalid) {
				t.Errorf("Bytes error = %v; want one matching core.ErrInvalid", err)
			}
		})
	}
}

func TestFieldsEqualTreatsNilAndEmptyAlike(t *testing.T) {
	for _, tc := range []struct {
		name string
		a    map[string]any
		b    map[string]any
		want bool
	}{
		{"both nil", nil, nil, true},
		{"nil and empty", nil, map[string]any{}, true},
		{"empty and nil", map[string]any{}, nil, true},
		{"equal", map[string]any{"a": 1}, map[string]any{"a": 1}, true},
		{"different value", map[string]any{"a": 1}, map[string]any{"a": 2}, false},
		{"missing key", map[string]any{"a": 1, "b": 2}, map[string]any{"a": 1}, false},
		{"equal nested", map[string]any{"a": map[string]any{"b": []any{1, "two"}}}, map[string]any{"a": map[string]any{"b": []any{1, "two"}}}, true},
		{"different nested", map[string]any{"a": map[string]any{"b": []any{1, "two"}}}, map[string]any{"a": map[string]any{"b": []any{1, "three"}}}, false},
		{"nil value and missing key", map[string]any{"a": nil}, map[string]any{}, false},
		// A block holding a NaN decodes to a NaN, and reflect.DeepEqual reports
		// NaN != NaN. Without this, an unedited document with a .nan value would
		// be re-encoded on every write, which is exactly what must not happen.
		{"nan", map[string]any{"a": math.NaN()}, map[string]any{"a": math.NaN()}, true},
		{"nested nan", map[string]any{"a": []any{math.NaN()}}, map[string]any{"a": []any{math.NaN()}}, true},
		{"nan and a number", map[string]any{"a": math.NaN()}, map[string]any{"a": 1.0}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := fieldsEqual(tc.a, tc.b); got != tc.want {
				t.Errorf("fieldsEqual = %t; want %t", got, tc.want)
			}
			if got := fieldsEqual(tc.b, tc.a); got != tc.want {
				t.Errorf("fieldsEqual, reversed, = %t; want %t", got, tc.want)
			}
		})
	}
}

// TestUnchangedNaNAndUnusualScalarsStillRoundTrip is the round-trip consequence of
// the NaN rule above, and of the other scalars a YAML encoder would love to
// normalise.
func TestUnchangedNaNAndUnusualScalarsStillRoundTrip(t *testing.T) {
	for _, doc := range []string{
		"---\nvalue: .nan\n---\nbody\n",
		"---\nvalue: .inf\n---\nbody\n",
		"---\nvalue: 010\n---\nbody\n",
		"---\nvalue: 'quoted'\n---\nbody\n",
		"---\nvalue: 2024-01-02\n---\nbody\n",
		"---\nvalue: null\n---\nbody\n",
	} {
		t.Run(strings.TrimSpace(doc), func(t *testing.T) {
			parsed, err := Parse([]byte(doc))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			out, err := parsed.Bytes()
			if err != nil {
				t.Fatalf("Bytes: %v", err)
			}
			if !bytes.Equal(out, []byte(doc)) {
				t.Errorf("an unedited document was re-encoded: %s", byteDiff([]byte(doc), out))
			}
		})
	}
}

// changedLines reports the 1-based line numbers that differ between before and
// after: the region between their common prefix and their common suffix. A pure
// insertion reports the inserted lines' numbers in after, and a pure deletion the
// deleted lines' numbers in before.
//
// The point of asserting on this rather than on the output is that the phase's claim
// is about the diff a commit would show, and eyeballing an encoder's output does not
// check it.
func changedLines(before, after []byte) []int {
	old := strings.Split(string(before), "\n")
	new := strings.Split(string(after), "\n")

	prefix := 0
	for prefix < len(old) && prefix < len(new) && old[prefix] == new[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(old)-prefix && suffix < len(new)-prefix && old[len(old)-1-suffix] == new[len(new)-1-suffix] {
		suffix++
	}

	var lines []int
	for i := range max(len(old)-prefix-suffix, len(new)-prefix-suffix) {
		lines = append(lines, prefix+i+1)
	}
	return lines
}

func TestChangedLines(t *testing.T) {
	for _, tc := range []struct {
		name   string
		before string
		after  string
		want   []int
	}{
		{"identical", "a\nb\nc\n", "a\nb\nc\n", nil},
		{"changed", "a\nb\nc\n", "a\nB\nc\n", []int{2}},
		{"inserted", "a\nc\n", "a\nb\nc\n", []int{2}},
		{"deleted", "a\nb\nc\n", "a\nc\n", []int{2}},
		{"appended at the end of a block", "a\nb\n---\n", "a\nb\nc\n---\n", []int{3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := changedLines([]byte(tc.before), []byte(tc.after)); !slices.Equal(got, tc.want) {
				t.Errorf("changedLines = %v; want %v", got, tc.want)
			}
		})
	}
}

// editOneField parses doc, changes exactly one existing scalar, and returns the input
// and output bytes.
func editOneField(t *testing.T, doc, key string, to any) (before, after []byte) {
	t.Helper()

	before = []byte(doc)
	parsed, err := Parse(before)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, ok := parsed.Content.FrontMatter.Fields[key]; !ok {
		t.Fatalf("the document has no %q field to edit", key)
	}
	parsed.Content.FrontMatter.Fields[key] = to

	after, err = parsed.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	return before, after
}

// TestEditingOneYAMLFieldChangesOnlyThatLine is the heart of the phase. The block
// deliberately contains everything an encoder would love to normalise: keys out of
// alphabetical order, a full-line comment, a trailing comment, a single-quoted value,
// an unquoted date, a nested mapping and a list.
func TestEditingOneYAMLFieldChangesOnlyThatLine(t *testing.T) {
	before, after := editOneField(t, awkward[core.FrontMatterYAML], "zebra", 2)

	if got := changedLines(before, after); !slices.Equal(got, []int{3}) {
		t.Errorf("changed lines = %v; want only line 3, the edited one.\nbefore:\n%s\nafter:\n%s", got, before, after)
	}
}

// TestEditingOneTOMLFieldAlsoRequotesTheOtherStrings names the one thing this
// package does worse than a one-line diff, so that it is visible in the test output
// rather than discovered in a commit. Editing zebra also rewrites title, because the
// TOML encoder emits a string as a literal string in single quotes wherever the
// content allows and offers no option to choose otherwise -- so `title = "Hello"`
// comes back as `title = 'Hello'`.
//
// It is bounded: the passthrough path never reaches an encoder and still moves no
// byte, only the quote characters change and never a value, and it is one-time per
// document, since a re-encoded block is already in the style the encoder emits. See
// the "Guarantees and limits" paragraph of doc.go, which records it for a reader of
// the package.
//
// The fixture keeps its double quotes deliberately. Rewriting it in single quotes
// would make this test pass with [2] again and would delete the only evidence in the
// suite that the requote happens.
func TestEditingOneTOMLFieldAlsoRequotesTheOtherStrings(t *testing.T) {
	before, after := editOneField(t, awkward[core.FrontMatterTOML], "zebra", int64(2))

	if got := changedLines(before, after); !slices.Equal(got, []int{2, 3}) {
		t.Errorf("changed lines = %v; want [2 3]: the edited line and the requoted title.\nbefore:\n%s\nafter:\n%s", got, before, after)
	}
}

// TestEditingOneTOMLFieldKeepsALocalDate covers the shape a Hugo document carries
// and the awkward fixture does not: a TOML local date, which has no offset and no
// clock. Editing a different field must leave it exactly as it was authored --
// a date that moves by a day is a wrong value written to a file, not a noisy diff,
// and it is reachable from Bytes by editing any other field of the document.
//
// It carries its own fixture because three tests above assert on awkward's line
// numbers, and adding an entry there would renumber them.
func TestEditingOneTOMLFieldKeepsALocalDate(t *testing.T) {
	const doc = "+++\ndate = 2024-01-02\ntitle = \"Hello\"\n+++\n# Title\n"

	before, after := editOneField(t, doc, "title", "Goodbye")

	if got := changedLines(before, after); !slices.Equal(got, []int{3}) {
		t.Errorf("changed lines = %v; want only line 3, the edited one: the date was rewritten.\nbefore:\n%s\nafter:\n%s", got, before, after)
	}
}

func TestEditingOneJSONFieldChangesOnlyThatLine(t *testing.T) {
	before, after := editOneField(t, awkward[core.FrontMatterJSON], "zebra", json.Number("2"))

	if got := changedLines(before, after); !slices.Equal(got, []int{2}) {
		t.Errorf("changed lines = %v; want only line 2, the edited one.\nbefore:\n%s\nafter:\n%s", got, before, after)
	}
}

func TestReencodingKeepsAuthoredKeyOrder(t *testing.T) {
	for _, tc := range []struct {
		format core.FrontMatterFormat
		to     any
		want   []string
	}{
		{core.FrontMatterYAML, 2, []string{"zebra", "title", "date", "author", "tags", "apple"}},
		{core.FrontMatterTOML, int64(2), []string{"zebra", "title", "apple"}},
		{core.FrontMatterJSON, json.Number("2"), []string{"zebra", "title", "apple"}},
	} {
		t.Run(tc.format.String(), func(t *testing.T) {
			_, after := editOneField(t, awkward[tc.format], "zebra", tc.to)

			reparsed, err := Parse(after)
			if err != nil {
				t.Fatalf("Parse of the re-encoded document: %v", err)
			}
			if got := emittedKeys(t, tc.format, reparsed.Content.FrontMatter.Raw); !slices.Equal(got, tc.want) {
				t.Errorf("emitted keys = %q; want the authored order %q", got, tc.want)
			}
		})
	}
}

// TestReencodingKeepsTheAuthoredOrderOfDottedKeys covers a defect in what the
// scanner replaced rather than anything the format does. toml.MetaData.Keys reports
// a dotted key as a path -- [banana x] for "banana.x = 2", and the same for a
// "[banana.x]" header -- and the old implementation filtered to paths of length one,
// so the top-level name never reached the authored order and both tables fell into
// orderedKeys' sorted tail. Measured against that implementation: the emitted keys
// came out zebra, apple, banana, alphabetical.
//
// What this does not change is that a dotted key is still re-emitted as a [table]
// header, because tomlKeyOrder classifies from the decoded value and a dotted key's
// top-level value is a map. That is the surrounding behaviour and not this test's
// subject.
func TestReencodingKeepsTheAuthoredOrderOfDottedKeys(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  string
	}{
		{"dotted-keys", "+++\nzebra = 1\nbanana.x = 2\napple.y = 3\n+++\n# Body\n"},
		{"dotted-headers", "+++\nzebra = 1\n\n[banana.x]\nn = 2\n\n[apple.y]\nn = 3\n+++\n# Body\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, after := editOneField(t, tc.doc, "zebra", int64(2))

			reparsed, err := Parse(after)
			if err != nil {
				t.Fatalf("Parse of the re-encoded document: %v", err)
			}
			want := []string{"zebra", "banana", "apple"}
			if got := emittedKeys(t, core.FrontMatterTOML, reparsed.Content.FrontMatter.Raw); !slices.Equal(got, want) {
				t.Errorf("emitted keys = %q; want the authored order %q.\nafter:\n%s", got, want, after)
			}
		})
	}
}

func TestReencodingKeepsYAMLCommentsAndScalarStyles(t *testing.T) {
	before, after := editOneField(t, awkward[core.FrontMatterYAML], "zebra", 2)

	for _, want := range []string{
		"# what this is",   // a full-line comment above another key
		"# the title",      // a trailing comment on an untouched line
		"title: 'Hello'",   // single quotes, not rewritten as double or bare
		"date: 2024-01-02", // an unquoted date, not an RFC 3339 timestamp
		"author:",
		"  - go", // the authored list indentation
	} {
		if !bytes.Contains(after, []byte(want)) {
			t.Errorf("the re-encoded document lost %q.\nbefore:\n%s\nafter:\n%s", want, before, after)
		}
	}
	if bytes.Contains(after, []byte("2024-01-02T")) {
		t.Errorf("the unquoted date was normalized to a timestamp.\nafter:\n%s", after)
	}
}

// TestReencodingKeepsAMultiLineBlockScalar covers the one shape awkward does not
// have: a literal block scalar, which the emitter writes through its own path
// rather than the ordinary scalar one. It carries its own fixture because three
// tests above assert on awkward's line numbers, and adding an entry there would
// renumber them.
func TestReencodingKeepsAMultiLineBlockScalar(t *testing.T) {
	const doc = "---\ntitle: Hello\nsummary: |\n  First line.\n  Second line.\n  Third line.\nauthor: Ada\n---\n# Body\n"

	before, after := editOneField(t, doc, "title", "Goodbye")

	if got := changedLines(before, after); !slices.Equal(got, []int{2}) {
		t.Errorf("changed lines = %v; want only line 2, the edited one.\nbefore:\n%s\nafter:\n%s", got, before, after)
	}
}

func TestReencodingAppendsNewKeysAfterTheAuthoredOnes(t *testing.T) {
	for _, tc := range []struct {
		format core.FrontMatterFormat
		want   []int
	}{
		// The new key lands on the line after the last authored one, which
		// pushes the closing delimiter down; only that one line is new.
		{core.FrontMatterYAML, []int{12}},
		// TOML needs three: the requoted title on line 3 and the new key on
		// line 5 sit apart, so the region between the common prefix and the
		// common suffix spans the untouched line between them. The requote is
		// the same cause as TestEditingOneTOMLFieldAlsoRequotesTheOtherStrings,
		// not a second one.
		{core.FrontMatterTOML, []int{3, 4, 5}},
		// JSON needs two: the added line, and a comma on the line that used to
		// be last. That is the format's own punctuation, not a rewrite.
		{core.FrontMatterJSON, []int{4, 5}},
	} {
		t.Run(tc.format.String(), func(t *testing.T) {
			before := []byte(awkward[tc.format])
			parsed, err := Parse(before)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			parsed.Content.FrontMatter.Fields["draft"] = true

			after, err := parsed.Bytes()
			if err != nil {
				t.Fatalf("Bytes: %v", err)
			}
			if got := changedLines(before, after); !slices.Equal(got, tc.want) {
				t.Errorf("changed lines = %v; want %v.\nbefore:\n%s\nafter:\n%s", got, tc.want, before, after)
			}

			reparsed, err := Parse(after)
			if err != nil {
				t.Fatalf("Parse of the re-encoded document: %v", err)
			}
			keys := emittedKeys(t, tc.format, reparsed.Content.FrontMatter.Raw)
			if len(keys) == 0 || keys[len(keys)-1] != "draft" {
				t.Errorf("emitted keys = %q; want the new key last", keys)
			}
		})
	}
}

func TestReencodingDropsOnlyTheRemovedKey(t *testing.T) {
	for _, tc := range []struct {
		format core.FrontMatterFormat
		want   []int
	}{
		{core.FrontMatterYAML, []int{4}},
		{core.FrontMatterTOML, []int{3}},
		{core.FrontMatterJSON, []int{3}},
	} {
		t.Run(tc.format.String(), func(t *testing.T) {
			before := []byte(awkward[tc.format])
			parsed, err := Parse(before)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			delete(parsed.Content.FrontMatter.Fields, "title")

			after, err := parsed.Bytes()
			if err != nil {
				t.Fatalf("Bytes: %v", err)
			}
			if got := changedLines(before, after); !slices.Equal(got, tc.want) {
				t.Errorf("changed lines = %v; want %v.\nbefore:\n%s\nafter:\n%s", got, tc.want, before, after)
			}
			if bytes.Contains(after, []byte("Hello")) {
				t.Errorf("the removed key survived.\nafter:\n%s", after)
			}
		})
	}
}

// TestReencodingLosesTOMLComments asserts a known limitation head-on, so that the
// wart is visible in the test output rather than discovered in production. No Go TOML
// library round-trips comments; preserving them would take a line-surgical patcher
// over the authored block.
func TestReencodingLosesTOMLComments(t *testing.T) {
	const doc = "+++\n# what this is\ntitle = \"Hello\"\nzebra = 1\n+++\n# Body\n"

	before, after := editOneField(t, doc, "zebra", int64(2))

	if bytes.Contains(before, []byte("# what this is")) && bytes.Contains(after, []byte("# what this is")) {
		t.Errorf("TOML comments now survive a re-encode; the documented limitation is out of date and the open question can be closed.\nafter:\n%s", after)
	}
	// The rest of the block still holds: order, and the untouched value.
	reparsed, err := Parse(after)
	if err != nil {
		t.Fatalf("Parse of the re-encoded document: %v", err)
	}
	if got := emittedKeys(t, core.FrontMatterTOML, reparsed.Content.FrontMatter.Raw); !slices.Equal(got, []string{"title", "zebra"}) {
		t.Errorf("emitted keys = %q; want the authored order", got)
	}
}
