package markdown

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/shutx-net/micawber/core"
)

func TestParseYAMLFrontMatter(t *testing.T) {
	doc, err := Parse([]byte("---\ntitle: Hello\ndraft: false\ncount: 3\ntags:\n  - go\n  - cms\n---\n# Title\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	fm := doc.Content.FrontMatter
	if fm.Format != core.FrontMatterYAML {
		t.Fatalf("Format = %q; want %q", fm.Format, core.FrontMatterYAML)
	}
	if got, ok := fm.Text("title"); !ok || got != "Hello" {
		t.Errorf(`Text("title") = (%q, %t); want ("Hello", true)`, got, ok)
	}
	if got, ok := fm.Bool("draft"); !ok || got {
		t.Errorf(`Bool("draft") = (%t, %t); want (false, true)`, got, ok)
	}
	if got, ok := fm.Lookup("count"); !ok || got != 3 {
		t.Errorf(`Lookup("count") = (%#v, %t); want (int 3, true)`, got, ok)
	}
	if got, ok := fm.Strings("tags"); !ok || !reflect.DeepEqual(got, []string{"go", "cms"}) {
		t.Errorf(`Strings("tags") = (%q, %t); want (["go" "cms"], true)`, got, ok)
	}
	if want := "# Title\n"; string(doc.Content.Body) != want {
		t.Errorf("Body = %q; want %q", doc.Content.Body, want)
	}
}

func TestParseYAMLKeepsRawVerbatim(t *testing.T) {
	tests := []struct {
		name      string
		doc       string
		wantOpen  string
		wantRaw   string
		wantClose string
		wantBody  string
	}{
		{
			name:      "lf",
			doc:       "---\ntitle: Hello\n---\nbody\n",
			wantOpen:  "---\n",
			wantRaw:   "title: Hello\n",
			wantClose: "---\n",
			wantBody:  "body\n",
		},
		{
			name:      "crlf",
			doc:       "---\r\ntitle: Hello\r\n---\r\nbody\r\n",
			wantOpen:  "---\r\n",
			wantRaw:   "title: Hello\r\n",
			wantClose: "---\r\n",
			wantBody:  "body\r\n",
		},
		{
			name:      "closer at eof",
			doc:       "---\ntitle: Hello\n---",
			wantOpen:  "---\n",
			wantRaw:   "title: Hello\n",
			wantClose: "---",
			wantBody:  "",
		},
		{
			name:      "trailing whitespace on the delimiters",
			doc:       "---  \ntitle: Hello\n---\t\nbody\n",
			wantOpen:  "---  \n",
			wantRaw:   "title: Hello\n",
			wantClose: "---\t\n",
			wantBody:  "body\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := Parse([]byte(tc.doc))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if doc.Layout.Open != tc.wantOpen {
				t.Errorf("Layout.Open = %q; want %q", doc.Layout.Open, tc.wantOpen)
			}
			if doc.Layout.Close != tc.wantClose {
				t.Errorf("Layout.Close = %q; want %q", doc.Layout.Close, tc.wantClose)
			}
			if got := string(doc.Content.FrontMatter.Raw); got != tc.wantRaw {
				t.Errorf("Raw = %q; want %q", got, tc.wantRaw)
			}
			if got := string(doc.Content.Body); got != tc.wantBody {
				t.Errorf("Body = %q; want %q", got, tc.wantBody)
			}
		})
	}
}

// TestParseYAMLDecodesCRLFAndLFAlike pins the rule that a decoder never sees a CR:
// a Windows checkout with core.autocrlf produces CRLF working-tree files, and they
// must decode to the same fields as their LF twin while keeping their own bytes.
func TestParseYAMLDecodesCRLFAndLFAlike(t *testing.T) {
	lf, err := Parse([]byte("---\ntitle: Hello\ntags:\n  - go\n---\nbody\n"))
	if err != nil {
		t.Fatalf("Parse LF: %v", err)
	}
	crlf, err := Parse([]byte("---\r\ntitle: Hello\r\ntags:\r\n  - go\r\n---\r\nbody\r\n"))
	if err != nil {
		t.Fatalf("Parse CRLF: %v", err)
	}

	if !reflect.DeepEqual(lf.Content.FrontMatter.Fields, crlf.Content.FrontMatter.Fields) {
		t.Errorf("CRLF fields differ from LF fields: %#v vs %#v", crlf.Content.FrontMatter.Fields, lf.Content.FrontMatter.Fields)
	}
	if bytes.Equal(lf.Content.FrontMatter.Raw, crlf.Content.FrontMatter.Raw) {
		t.Errorf("Raw was normalized: the CRLF document must keep its own bytes")
	}
	if got := crlf.Layout.lineEnding(); got != "\r\n" {
		t.Errorf("lineEnding() = %q; want %q", got, "\r\n")
	}
}

func TestParseYAMLAcceptsTrailingWhitespaceOnDelimiterLines(t *testing.T) {
	for _, doc := range []string{
		"--- \ntitle: Hello\n---\n",
		"---\t\ntitle: Hello\n--- \t \n",
		"--- \r\ntitle: Hello\r\n--- \r\n",
	} {
		t.Run(strings.ReplaceAll(doc, "\n", `\n`), func(t *testing.T) {
			parsed, err := Parse([]byte(doc))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if parsed.Content.FrontMatter.Format != core.FrontMatterYAML {
				t.Errorf("Format = %q; want %q", parsed.Content.FrontMatter.Format, core.FrontMatterYAML)
			}
			if got, ok := parsed.Content.FrontMatter.Text("title"); !ok || got != "Hello" {
				t.Errorf(`Text("title") = (%q, %t); want ("Hello", true)`, got, ok)
			}
			out, err := parsed.Bytes()
			if err != nil {
				t.Fatalf("Bytes: %v", err)
			}
			if !bytes.Equal(out, []byte(doc)) {
				t.Errorf("round trip changed the document: %s", byteDiff([]byte(doc), out))
			}
		})
	}
}

func TestParseYAMLRejectsABlockThatIsNotAMapping(t *testing.T) {
	cdc, ok := codecFor(core.FrontMatterYAML)
	if !ok {
		t.Fatalf("codecFor(%q) reported no codec", core.FrontMatterYAML)
	}

	for _, block := range []string{"- one\n- two\n", "just a scalar\n", "42\n"} {
		t.Run(strings.TrimSpace(block), func(t *testing.T) {
			if _, err := cdc.decode([]byte(block)); !errors.Is(err, core.ErrInvalid) {
				t.Errorf("decode error = %v; want one matching core.ErrInvalid", err)
			} else if !strings.Contains(err.Error(), "mapping") {
				t.Errorf("decode error = %v; want a message saying the block is not a mapping", err)
			}
		})
	}

	if _, err := Parse([]byte("---\n- one\n- two\n---\nbody\n")); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("Parse error = %v; want one matching core.ErrInvalid", err)
	}
}

func TestParseYAMLRejectsAMalformedBlock(t *testing.T) {
	for _, doc := range []string{
		"---\ntitle: [unclosed\n---\nbody\n",
		"---\ntitle: Hello\n  indented: badly\n---\nbody\n",
		"---\n\ttab: indented\n---\nbody\n",
	} {
		t.Run(strings.ReplaceAll(doc, "\n", `\n`), func(t *testing.T) {
			if _, err := Parse([]byte(doc)); !errors.Is(err, core.ErrInvalid) {
				t.Errorf("Parse error = %v; want one matching core.ErrInvalid", err)
			}
		})
	}
}

// TestParseRejectsAnUnhashableMergeKeyWithoutPanicking pins a property of the
// package rather than of one library: whatever a block contains, Parse reports it,
// and a caller never has to recover from a decoder. Resolving a "<<" merge onto an
// unhashable key is the shape that broke it -- the resolved keys go into a map, and
// hashing an unhashable one panics past Unmarshal and past Parse -- so the
// assertion that matters here is the one the test makes by existing: Parse returns.
func TestParseRejectsAnUnhashableMergeKeyWithoutPanicking(t *testing.T) {
	const block = "a:\n<<:\n-\n?\n-\n"

	_, err := Parse([]byte("---\n" + block + "---\nbody\n"))
	if !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Parse error = %v; want one matching core.ErrInvalid", err)
	}
	for _, line := range strings.Split(block, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		if strings.Contains(err.Error(), line) {
			t.Errorf("the error echoes %q, a line of the block: %v", line, err)
		}
	}
}

func TestParseYAMLAcceptsAByteOrderMarkBeforeTheDelimiter(t *testing.T) {
	in := []byte("\ufeff---\ntitle: Hello\n---\nbody\n")

	doc, err := Parse(in)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Content.FrontMatter.Format != core.FrontMatterYAML {
		t.Errorf("Format = %q; want %q", doc.Content.FrontMatter.Format, core.FrontMatterYAML)
	}
	if !doc.Layout.BOM {
		t.Errorf("Layout.BOM = false; want true")
	}
	if doc.Layout.Open != "---\n" {
		t.Errorf("Layout.Open = %q; want %q: the mark is recorded in Layout.BOM, not in Open", doc.Layout.Open, "---\n")
	}
	out, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	if !bytes.Equal(out, in) {
		t.Errorf("the byte-order mark was not re-emitted: %s", byteDiff(in, out))
	}
}

func TestParseYAMLEmptyBlock(t *testing.T) {
	doc, err := Parse([]byte("---\n---\nbody\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	fm := doc.Content.FrontMatter
	if fm.Format != core.FrontMatterYAML {
		t.Errorf("Format = %q; want %q", fm.Format, core.FrontMatterYAML)
	}
	if len(fm.Raw) != 0 {
		t.Errorf("Raw = %q; want empty", fm.Raw)
	}
	if fm.Fields == nil {
		t.Errorf("Fields = nil; want an empty, non-nil map")
	}
	if len(fm.Fields) != 0 {
		t.Errorf("Fields has %d entries; want none", len(fm.Fields))
	}
	if fm.IsZero() {
		t.Errorf("FrontMatter.IsZero() = true; want false: the document has an empty block, not no block")
	}
	if doc.Layout.IsZero() {
		t.Errorf("Layout.IsZero() = true; want false")
	}
}
