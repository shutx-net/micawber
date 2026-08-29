package markdown

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/shutx-net/micawber/core"
)

func TestParseJSONFrontMatter(t *testing.T) {
	doc, err := Parse([]byte("{\n  \"title\": \"Hello\",\n  \"draft\": false,\n  \"tags\": [\"go\", \"cms\"]\n}\n# Title\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	fm := doc.Content.FrontMatter
	if fm.Format != core.FrontMatterJSON {
		t.Fatalf("Format = %q; want %q", fm.Format, core.FrontMatterJSON)
	}
	if got, ok := fm.Text("title"); !ok || got != "Hello" {
		t.Errorf(`Text("title") = (%q, %t); want ("Hello", true)`, got, ok)
	}
	if got, ok := fm.Bool("draft"); !ok || got {
		t.Errorf(`Bool("draft") = (%t, %t); want (false, true)`, got, ok)
	}
	if got, ok := fm.Strings("tags"); !ok || !reflect.DeepEqual(got, []string{"go", "cms"}) {
		t.Errorf(`Strings("tags") = (%q, %t); want (["go" "cms"], true)`, got, ok)
	}
	if want := "# Title\n"; string(doc.Content.Body) != want {
		t.Errorf("Body = %q; want %q", doc.Content.Body, want)
	}
}

// TestParseJSONKeepsRawVerbatimIncludingBraces pins the decision that Raw is the
// whole object text: JSON front matter has no delimiter lines for core's contract to
// exclude, and the braces are part of the value rather than a fence around it.
func TestParseJSONKeepsRawVerbatimIncludingBraces(t *testing.T) {
	tests := []struct {
		name      string
		doc       string
		wantRaw   string
		wantClose string
		wantBody  string
	}{
		{
			name:      "lf",
			doc:       "{\n  \"title\": \"Hello\"\n}\nbody\n",
			wantRaw:   "{\n  \"title\": \"Hello\"\n}",
			wantClose: "\n",
			wantBody:  "body\n",
		},
		{
			name:      "crlf",
			doc:       "{\r\n  \"title\": \"Hello\"\r\n}\r\nbody\r\n",
			wantRaw:   "{\r\n  \"title\": \"Hello\"\r\n}",
			wantClose: "\r\n",
			wantBody:  "body\r\n",
		},
		{
			name:      "trailing whitespace after the object",
			doc:       "{\"title\": \"Hello\"} \t\nbody\n",
			wantRaw:   "{\"title\": \"Hello\"}",
			wantClose: " \t\n",
			wantBody:  "body\n",
		},
		{
			name:      "object runs to the end of the file",
			doc:       "{\"title\": \"Hello\"}",
			wantRaw:   "{\"title\": \"Hello\"}",
			wantClose: "",
			wantBody:  "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := Parse([]byte(tc.doc))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if doc.Layout.Open != "" {
				t.Errorf("Layout.Open = %q; want empty: JSON front matter has no opening delimiter line", doc.Layout.Open)
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

			// Raw must still decode on its own, which is the property core's
			// contract rests on when it calls Raw the verbatim block.
			cdc, ok := codecFor(core.FrontMatterJSON)
			if !ok {
				t.Fatalf("codecFor(%q) reported no codec", core.FrontMatterJSON)
			}
			fields, err := cdc.decode(doc.Content.FrontMatter.Raw)
			if err != nil {
				t.Fatalf("decode of Raw on its own: %v", err)
			}
			if fields["title"] != "Hello" {
				t.Errorf(`decode of Raw gave %#v; want title "Hello"`, fields)
			}
		})
	}
}

// TestParseJSONKeepsNumbersAsWrittenText is why the decoder runs with UseNumber: a
// number that came back as a float64 would be re-emitted with a different number of
// digits, and an unedited write must not touch a byte.
func TestParseJSONKeepsNumbersAsWrittenText(t *testing.T) {
	in := []byte("{\n  \"big\": 123456789012345678901234567890,\n  \"precise\": 0.1000000000000000055511151231257827\n}\nbody\n")

	doc, err := Parse(in)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	for key, want := range map[string]string{
		"big":     "123456789012345678901234567890",
		"precise": "0.1000000000000000055511151231257827",
	} {
		number, ok := doc.Content.FrontMatter.Fields[key].(json.Number)
		if !ok {
			t.Errorf("Fields[%q] = %#v; want json.Number", key, doc.Content.FrontMatter.Fields[key])
			continue
		}
		if number.String() != want {
			t.Errorf("Fields[%q] = %s; want the source text %s", key, number, want)
		}
	}

	out, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	if !bytes.Equal(out, in) {
		t.Errorf("round trip changed the numbers: %s", byteDiff(in, out))
	}
}

func TestParseJSONRequiresTheClosingLineToBeBlankAfterTheObject(t *testing.T) {
	tests := []struct {
		name       string
		doc        string
		wantFormat core.FrontMatterFormat
	}{
		{"nothing after the brace", "{\"a\": 1}", core.FrontMatterJSON},
		{"spaces and a tab after the brace", "{\"a\": 1} \t\nbody\n", core.FrontMatterJSON},
		{"a CR before the terminator", "{\"a\": 1}\r\nbody\n", core.FrontMatterJSON},
		{"prose after the brace", "{\"a\": 1} and then prose\nbody\n", core.FrontMatterNone},
		{"a second value after the brace", "{\"a\": 1} {\"b\": 2}\n", core.FrontMatterNone},
		{"a comma after the brace", "{\"a\": 1},\n", core.FrontMatterNone},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := Parse([]byte(tc.doc))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if doc.Content.FrontMatter.Format != tc.wantFormat {
				t.Errorf("Format = %q; want %q", doc.Content.FrontMatter.Format, tc.wantFormat)
			}
			out, err := doc.Bytes()
			if err != nil {
				t.Fatalf("Bytes: %v", err)
			}
			if !bytes.Equal(out, []byte(tc.doc)) {
				t.Errorf("round trip changed the document: %s", byteDiff([]byte(tc.doc), out))
			}
		})
	}
}

// TestParseJSONWithATruncatedObjectHasNoFrontMatter covers the weak-signal rule, and
// contrasts it with the strong-signal rule for "---" so that the asymmetry is tested
// rather than merely documented.
func TestParseJSONWithATruncatedObjectHasNoFrontMatter(t *testing.T) {
	in := []byte("{\n  \"title\": \"Hello\"\n\nSome prose that never closes the object.\n")

	doc, err := Parse(in)
	if err != nil {
		t.Fatalf("Parse: %v; a leading brace is a weak signal, so it must degrade rather than fail", err)
	}
	if doc.Content.FrontMatter.Format != core.FrontMatterNone {
		t.Errorf("Format = %q; want %q", doc.Content.FrontMatter.Format, core.FrontMatterNone)
	}
	if !bytes.Equal(doc.Content.Body, in) {
		t.Errorf("Body is not the whole document: %s", byteDiff(in, doc.Content.Body))
	}
	if !doc.Layout.IsZero() {
		t.Errorf("Layout = %+v; want the zero value", doc.Layout)
	}

	// The same shape of damage in a "---" block is an error, because there a
	// first line of "---" means front matter and nothing else.
	if _, err := Parse([]byte("---\ntitle: Hello\n\nSome prose.\n")); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("Parse of an unterminated YAML block: error = %v; want one matching core.ErrInvalid", err)
	}
}

func TestParseJSONWithATrailingNonObjectHasNoFrontMatter(t *testing.T) {
	for _, doc := range []string{
		"{ this is not JSON at all\n\nprose\n",
		"{}}\nprose\n",
		"{\"unterminated string\": \"oh no\n}\nprose\n",
		"{\"a\": 1} trailing prose\n",
	} {
		t.Run(strings.ReplaceAll(doc, "\n", `\n`), func(t *testing.T) {
			parsed, err := Parse([]byte(doc))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if parsed.Content.FrontMatter.Format != core.FrontMatterNone {
				t.Errorf("Format = %q; want %q", parsed.Content.FrontMatter.Format, core.FrontMatterNone)
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

func TestJSONTopLevelKeyOrderIsRecoveredFromTheBlock(t *testing.T) {
	order, err := jsonTopLevelKeys([]byte("{\n  \"zebra\": 1,\n  \"apple\": {\n    \"nested\": true\n  },\n  \"mango\": [1, 2, 3]\n}"))
	if err != nil {
		t.Fatalf("jsonTopLevelKeys: %v", err)
	}
	if want := []string{"zebra", "apple", "mango"}; !reflect.DeepEqual(order, want) {
		t.Errorf("order = %q; want %q", order, want)
	}

	order, err = jsonTopLevelKeys([]byte("{}"))
	if err != nil {
		t.Fatalf("jsonTopLevelKeys of an empty object: %v", err)
	}
	if len(order) != 0 {
		t.Errorf("order = %q; want none", order)
	}
}
