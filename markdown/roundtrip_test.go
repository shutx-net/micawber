package markdown

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/shutx-net/micawber/core"
)

// corpusDoc is one document of the round-trip corpus: a name for the subtest, the
// front-matter format Parse is expected to detect, and the document itself.
//
// The documents are Go string literals rather than files under testdata/ on purpose.
// A CRLF, NUL or byte-order-mark fixture on disk is exactly what an autocrlf setting,
// an editor or a missing .gitattributes entry rewrites, and the test would then keep
// passing while asserting something else. An escape in source cannot be rewritten
// silently, and the same table seeds the fuzz target without a loader.
type corpusDoc struct {
	name   string
	format core.FrontMatterFormat
	doc    string
}

// corpus is the whole round-trip corpus. Every step of this phase extends it, and
// TestRoundTripIsByteIdentical, TestParseIsIdempotent and FuzzParseBytesRoundTrip all
// read from it, so a document added here is checked by all three at once.
var corpus = []corpusDoc{
	// Documents with no front matter: nothing on the first line opens a block, so
	// every byte is body.
	{"empty", core.FrontMatterNone, ""},
	{"body-only", core.FrontMatterNone, "# Title\n\nSome prose.\n"},
	{"body-only-no-trailing-newline", core.FrontMatterNone, "# Title\n\nSome prose."},
	{"body-only-crlf", core.FrontMatterNone, "# Title\r\n\r\nSome prose.\r\n"},
	{"body-only-bom", core.FrontMatterNone, "\ufeff# Title\n\nSome prose.\n"},
	{"body-only-unicode", core.FrontMatterNone, "# 見出し\n\nこんにちは、世界 🌸\n"},
	{"body-with-delimiter-line-later", core.FrontMatterNone, "Intro.\n\n---\n\nMore prose.\n"},
	{"body-only-nul", core.FrontMatterNone, "prose\x00more prose\n"},
	{"body-only-invalid-utf8", core.FrontMatterNone, "prose \xff more\n"},

	// YAML: the "---" block every Markdown tool in the ecosystem reads.
	{"yaml-basic", core.FrontMatterYAML, "---\ntitle: Hello\ndraft: false\n---\n# Title\n\nSome prose.\n"},
	{"yaml-empty-block", core.FrontMatterYAML, "---\n---\n# Title\n"},
	{"yaml-crlf", core.FrontMatterYAML, "---\r\ntitle: Hello\r\ndraft: false\r\n---\r\n# Title\r\n"},
	{"yaml-bom", core.FrontMatterYAML, "\ufeff---\ntitle: Hello\n---\n# Title\n"},
	{"yaml-no-trailing-newline", core.FrontMatterYAML, "---\ntitle: Hello\n---\n# Title"},
	{"yaml-closer-at-eof", core.FrontMatterYAML, "---\ntitle: Hello\n---"},
	{"yaml-no-body", core.FrontMatterYAML, "---\ntitle: Hello\n---\n"},
	{"yaml-unicode", core.FrontMatterYAML, "---\ntitle: 見出し\ntags:\n  - 🌸\n---\nこんにちは、世界\n"},
	{"yaml-body-contains-delimiter", core.FrontMatterYAML, "---\ntitle: Hello\n---\nIntro.\n\n---\n\nMore prose.\n"},
	{"yaml-delimiter-trailing-spaces", core.FrontMatterYAML, "---  \ntitle: Hello\n---\t\n# Title\n"},
	{"yaml-comments-and-blank-lines", core.FrontMatterYAML, "---\n# what this is\n\ntitle: Hello\n\n# and a trailing note\n---\n# Title\n"},
	{"yaml-nested-and-list", core.FrontMatterYAML, "---\ntitle: Hello\nauthor:\n  name: Ada\n  email: ada@example.com\ntags:\n  - go\n  - cms\n---\n# Title\n"},
	{"yaml-block-scalar", core.FrontMatterYAML, "---\ntitle: Hello\nsummary: |\n  First line.\n  Second line.\n  Third line.\nauthor: Ada\n---\n# Title\n"},
	{"yaml-nul-in-body", core.FrontMatterYAML, "---\ntitle: Hello\n---\nprose\x00more prose\n"},

	// TOML: Hugo's "+++" block.
	{"toml-basic", core.FrontMatterTOML, "+++\ntitle = \"Hello\"\ndraft = false\n+++\n# Title\n\nSome prose.\n"},
	{"toml-empty-block", core.FrontMatterTOML, "+++\n+++\n# Title\n"},
	{"toml-crlf", core.FrontMatterTOML, "+++\r\ntitle = \"Hello\"\r\ndraft = false\r\n+++\r\n# Title\r\n"},
	{"toml-with-table", core.FrontMatterTOML, "+++\ntitle = \"Hello\"\ntags = [\"go\", \"cms\"]\n\n[author]\nname = \"Ada\"\n\n[[links]]\nurl = \"https://example.com\"\n+++\n# Title\n"},
	{"toml-no-trailing-newline", core.FrontMatterTOML, "+++\ntitle = \"Hello\"\n+++\n# Title"},
	{"toml-local-date", core.FrontMatterTOML, "+++\ndate = 2024-01-02\ntitle = \"Hello\"\n+++\n# Title\n"},

	// JSON: a leading object, which is front matter only when it confirms itself
	// completely. json-not-an-object is the degrade path, and it is in the corpus
	// because that path must round-trip exactly too.
	{"json-basic", core.FrontMatterJSON, "{\n  \"title\": \"Hello\",\n  \"draft\": false\n}\n# Title\n\nSome prose.\n"},
	{"json-nested", core.FrontMatterJSON, "{\n  \"title\": \"Hello\",\n  \"author\": {\n    \"name\": \"Ada\"\n  },\n  \"tags\": [\"go\", \"cms\"]\n}\n# Title\n"},
	{"json-crlf", core.FrontMatterJSON, "{\r\n  \"title\": \"Hello\"\r\n}\r\n# Title\r\n"},
	{"json-no-body", core.FrontMatterJSON, "{\n  \"title\": \"Hello\"\n}\n"},
	{"json-object-runs-to-eof", core.FrontMatterJSON, "{\"title\": \"Hello\"}"},
	{"json-trailing-space-after-object", core.FrontMatterJSON, "{\"title\": \"Hello\"} \t\n# Title\n"},
	{"json-not-an-object", core.FrontMatterNone, "{ this is not JSON at all\n\nSome prose.\n"},
}

// byteDiff summarises how two documents differ without printing either of them: a
// failing document may hold anything its author put in it, up to a credential, so the
// test output names offsets and lengths only.
func byteDiff(want, got []byte) string {
	for i := range min(len(want), len(got)) {
		if want[i] != got[i] {
			return fmt.Sprintf("first difference at byte %d (want %#x, got %#x); lengths %d and %d", i, want[i], got[i], len(want), len(got))
		}
	}
	if len(want) == len(got) {
		return "no difference"
	}
	return fmt.Sprintf("identical for the first %d bytes; lengths %d and %d", min(len(want), len(got)), len(want), len(got))
}

func TestLayoutZeroValueIsCanonical(t *testing.T) {
	var zero Layout

	if !zero.IsZero() {
		t.Errorf("Layout{}.IsZero() = false; want true")
	}
	if got := zero.lineEnding(); got != "\n" {
		t.Errorf("Layout{}.lineEnding() = %q; want %q", got, "\n")
	}
	if !defaultLayout(core.FrontMatterNone).IsZero() {
		t.Errorf("defaultLayout(FrontMatterNone).IsZero() = false; want true")
	}

	for _, tc := range []struct {
		name   string
		layout Layout
	}{
		{"bom", Layout{BOM: true}},
		{"open", Layout{Open: "---\n"}},
		{"close", Layout{Close: "---\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.layout.IsZero() {
				t.Errorf("IsZero() = true; want false when %s is set", tc.name)
			}
		})
	}

	// A zero Layout contributes no bytes: a document without front matter
	// serializes to its body alone.
	body := []byte("# Title\n")
	out, err := Document{Content: core.Content{Body: body}}.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	if !bytes.Equal(out, body) {
		t.Errorf("a zero Layout contributed bytes: %s", byteDiff(body, out))
	}

	// And for a document that does have front matter, the zero Layout means the
	// canonical delimiters, so a Document assembled by hand still serializes to a
	// document that parses back.
	hand := Document{Content: core.Content{
		FrontMatter: core.FrontMatter{
			Format: core.FrontMatterYAML,
			Raw:    []byte("title: Hello\n"),
			Fields: map[string]any{"title": "Hello"},
		},
		Body: body,
	}}
	out, err = hand.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	want := []byte("---\ntitle: Hello\n---\n# Title\n")
	if !bytes.Equal(out, want) {
		t.Errorf("zero Layout did not supply the canonical delimiters: %s", byteDiff(want, out))
	}
}

// TestDocumentBytesConcatenatesLayoutRawAndBody asserts the identity the whole phase
// rests on -- output is BOM, Open, Raw, Close, Body concatenated in that order -- on a
// Document built by hand, so it is checked directly and not only through Parse. The
// delimiter lines carry trailing whitespace to show they are emitted verbatim rather
// than regenerated.
func TestDocumentBytesConcatenatesLayoutRawAndBody(t *testing.T) {
	doc := Document{
		Content: core.Content{
			FrontMatter: core.FrontMatter{
				Format: core.FrontMatterYAML,
				Raw:    []byte("title: Hello\n"),
				Fields: map[string]any{"title": "Hello"},
			},
			Body: []byte("Body text.\n"),
		},
		Layout: Layout{BOM: true, Open: "---  \n", Close: "---\t\n"},
	}

	want := []byte("\ufeff" + "---  \n" + "title: Hello\n" + "---\t\n" + "Body text.\n")
	got, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("Bytes is not the concatenation of the five slots: %s", byteDiff(want, got))
	}
}

func TestParseWithoutFrontMatterPutsEverythingInTheBody(t *testing.T) {
	for _, tc := range corpus {
		if tc.format != core.FrontMatterNone {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			in := []byte(tc.doc)
			doc, err := Parse(in)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}

			fm := doc.Content.FrontMatter
			if fm.Format != core.FrontMatterNone {
				t.Errorf("Format = %q; want %q", fm.Format, core.FrontMatterNone)
			}
			if fm.Raw != nil {
				t.Errorf("Raw is %d bytes; want nil", len(fm.Raw))
			}
			if fm.Fields != nil {
				t.Errorf("Fields has %d entries; want nil", len(fm.Fields))
			}
			if !doc.Layout.IsZero() {
				t.Errorf("Layout = %+v; want the zero value", doc.Layout)
			}
			if !bytes.Equal(doc.Content.Body, in) {
				t.Errorf("Body is not the whole document: %s", byteDiff(in, doc.Content.Body))
			}
		})
	}
}

func TestFormatWithoutFrontMatterReturnsTheBody(t *testing.T) {
	body := []byte("# Title\n\nSome prose.\n")

	got, err := Format(core.Content{Body: body})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("Format changed a document with no front matter: %s", byteDiff(body, got))
	}

	got, err = Format(core.Content{})
	if err != nil {
		t.Fatalf("Format of the zero Content: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Format of the zero Content returned %d bytes; want none", len(got))
	}
}

func TestRoundTripIsByteIdentical(t *testing.T) {
	for _, tc := range corpus {
		t.Run(tc.name, func(t *testing.T) {
			in := []byte(tc.doc)

			doc, err := Parse(in)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if doc.Content.FrontMatter.Format != tc.format {
				t.Errorf("Format = %q; want %q", doc.Content.FrontMatter.Format, tc.format)
			}

			out, err := doc.Bytes()
			if err != nil {
				t.Fatalf("Bytes: %v", err)
			}
			if !bytes.Equal(out, in) {
				t.Errorf("parsing and serializing changed the document: %s", byteDiff(in, out))
			}
		})
	}
}

func TestParseIsIdempotent(t *testing.T) {
	for _, tc := range corpus {
		t.Run(tc.name, func(t *testing.T) {
			first, err := Parse([]byte(tc.doc))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			out, err := first.Bytes()
			if err != nil {
				t.Fatalf("Bytes: %v", err)
			}
			second, err := Parse(out)
			if err != nil {
				t.Fatalf("Parse of the serialized document: %v", err)
			}

			if second.Layout != first.Layout {
				t.Errorf("Layout = %+v; want %+v", second.Layout, first.Layout)
			}
			if second.Content.FrontMatter.Format != first.Content.FrontMatter.Format {
				t.Errorf("Format = %q; want %q", second.Content.FrontMatter.Format, first.Content.FrontMatter.Format)
			}
			if !bytes.Equal(second.Content.FrontMatter.Raw, first.Content.FrontMatter.Raw) {
				t.Errorf("Raw changed: %s", byteDiff(first.Content.FrontMatter.Raw, second.Content.FrontMatter.Raw))
			}
			if !bytes.Equal(second.Content.Body, first.Content.Body) {
				t.Errorf("Body changed: %s", byteDiff(first.Content.Body, second.Content.Body))
			}
		})
	}
}
