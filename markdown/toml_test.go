package markdown

import (
	"bytes"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shutx-net/micawber/core"
)

// TestParseTOMLFrontMatter writes down the Go types a TOML block decodes to,
// rather than leaving the heterogeneity between the codecs to be discovered later:
// TOML integers are int64 where YAML's are int, and an offset date-time is a
// time.Time.
//
// Two of those types are the TOML library's own rather than the standard library's.
// An array of tables decodes to []any, not []map[string]any -- see
// TestParseTOMLWithTables. A local date, local date-time or local time decodes to a
// toml.LocalDate, toml.LocalDateTime or toml.LocalTime rather than to a time.Time,
// which is what lets `date = 2024-01-02` survive an edit to another field byte for
// byte, in every zone; the cost is that core.FrontMatter.Time does not recognise it,
// since that accepts a time.Time or a string in one of three layouts.
func TestParseTOMLFrontMatter(t *testing.T) {
	doc, err := Parse([]byte("+++\ntitle = \"Hello\"\ncount = 3\ndraft = false\ndate = 2024-01-02T15:04:05Z\ntags = [\"go\", \"cms\"]\n+++\n# Title\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	fm := doc.Content.FrontMatter
	if fm.Format != core.FrontMatterTOML {
		t.Fatalf("Format = %q; want %q", fm.Format, core.FrontMatterTOML)
	}
	if got, ok := fm.Text("title"); !ok || got != "Hello" {
		t.Errorf(`Text("title") = (%q, %t); want ("Hello", true)`, got, ok)
	}
	if got, ok := fm.Lookup("count"); !ok || got != int64(3) {
		t.Errorf(`Lookup("count") = %#v; want int64(3)`, got)
	}
	if got, ok := fm.Bool("draft"); !ok || got {
		t.Errorf(`Bool("draft") = (%t, %t); want (false, true)`, got, ok)
	}
	if got, ok := fm.Fields["date"].(time.Time); !ok || !got.Equal(time.Date(2024, 1, 2, 15, 4, 5, 0, time.UTC)) {
		t.Errorf(`Fields["date"] = %#v; want a time.Time of 2024-01-02T15:04:05Z`, fm.Fields["date"])
	}
	if got, ok := fm.Strings("tags"); !ok || !reflect.DeepEqual(got, []string{"go", "cms"}) {
		t.Errorf(`Strings("tags") = (%q, %t); want (["go" "cms"], true)`, got, ok)
	}
	if want := "# Title\n"; string(doc.Content.Body) != want {
		t.Errorf("Body = %q; want %q", doc.Content.Body, want)
	}
}

func TestParseTOMLKeepsRawVerbatim(t *testing.T) {
	in := []byte("+++  \ntitle = \"Hello\"\n+++\t\nbody\n")

	doc, err := Parse(in)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Layout.Open != "+++  \n" {
		t.Errorf("Layout.Open = %q; want %q", doc.Layout.Open, "+++  \n")
	}
	if doc.Layout.Close != "+++\t\n" {
		t.Errorf("Layout.Close = %q; want %q", doc.Layout.Close, "+++\t\n")
	}
	if got := string(doc.Content.FrontMatter.Raw); got != "title = \"Hello\"\n" {
		t.Errorf("Raw = %q; want %q", got, "title = \"Hello\"\n")
	}
	out, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	if !bytes.Equal(out, in) {
		t.Errorf("round trip changed the document: %s", byteDiff(in, out))
	}
}

func TestParseTOMLDecodesCRLFAndLFAlike(t *testing.T) {
	lf, err := Parse([]byte("+++\ntitle = \"Hello\"\ntags = [\"go\"]\n+++\nbody\n"))
	if err != nil {
		t.Fatalf("Parse LF: %v", err)
	}
	crlf, err := Parse([]byte("+++\r\ntitle = \"Hello\"\r\ntags = [\"go\"]\r\n+++\r\nbody\r\n"))
	if err != nil {
		t.Fatalf("Parse CRLF: %v", err)
	}

	if !reflect.DeepEqual(lf.Content.FrontMatter.Fields, crlf.Content.FrontMatter.Fields) {
		t.Errorf("CRLF fields differ from LF fields: %#v vs %#v", crlf.Content.FrontMatter.Fields, lf.Content.FrontMatter.Fields)
	}
	if bytes.Equal(lf.Content.FrontMatter.Raw, crlf.Content.FrontMatter.Raw) {
		t.Errorf("Raw was normalized: the CRLF document must keep its own bytes")
	}
}

func TestParseTOMLWithTables(t *testing.T) {
	doc, err := Parse([]byte("+++\ntitle = \"Hello\"\n\n[author]\nname = \"Ada\"\n\n[[links]]\nurl = \"https://example.com\"\n+++\nbody\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	fields := doc.Content.FrontMatter.Fields
	author, ok := fields["author"].(map[string]any)
	if !ok {
		t.Fatalf(`Fields["author"] = %#v; want map[string]any`, fields["author"])
	}
	if author["name"] != "Ada" {
		t.Errorf(`Fields["author"]["name"] = %#v; want "Ada"`, author["name"])
	}

	// An array of tables is a []any of maps, not a []map[string]any. Both
	// isTOMLTable and valuesEqual keep their []map[string]any arms all the same,
	// because a caller assembling a Content by hand can still build one.
	links, ok := fields["links"].([]any)
	if !ok {
		t.Fatalf(`Fields["links"] = %#v; want []any`, fields["links"])
	}
	if len(links) != 1 {
		t.Fatalf(`Fields["links"] = %#v; want one table`, links)
	}
	link, ok := links[0].(map[string]any)
	if !ok {
		t.Fatalf(`Fields["links"][0] = %#v; want map[string]any`, links[0])
	}
	if link["url"] != "https://example.com" {
		t.Errorf(`Fields["links"][0]["url"] = %#v; want the url`, link["url"])
	}
}

func TestParseTOMLRejectsAMalformedBlock(t *testing.T) {
	for _, doc := range []string{
		"+++\ntitle = \n+++\nbody\n",
		"+++\ntitle = \"unclosed\n+++\nbody\n",
		"+++\nnot toml at all\n+++\nbody\n",
		"+++\ntitle = \"Hello\"\ntitle = \"Again\"\n+++\nbody\n",
	} {
		t.Run(strings.ReplaceAll(doc, "\n", `\n`), func(t *testing.T) {
			if _, err := Parse([]byte(doc)); !errors.Is(err, core.ErrInvalid) {
				t.Errorf("Parse error = %v; want one matching core.ErrInvalid", err)
			}
		})
	}
}

// TestParseRejectsAMalformedTOMLBlockWithoutEchoingIt is the TOML half of the rule
// TestParseRejectsAnUnhashableMergeKeyWithoutPanicking pins for YAML: the error says
// where the block is broken and never what it holds.
//
// The credential-shaped value sits on the line before the broken one deliberately.
// That is exactly where a TOML library's own rendering of the failure would print
// it -- the offending line is shown together with every line above it -- so this
// test is what stops a maintainer wiring that rendering in.
func TestParseRejectsAMalformedTOMLBlockWithoutEchoingIt(t *testing.T) {
	const block = "ok = 1\napi_token = \"sk-SECRET-abc123\"\nbad = \n"

	_, err := Parse([]byte("+++\n" + block + "+++\nbody\n"))
	if !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Parse error = %v; want one matching core.ErrInvalid", err)
	}
	if want := "block line 3"; !strings.Contains(err.Error(), want) {
		t.Errorf("Parse error = %v; want a message naming %q, where the block is broken", err, want)
	}
	if strings.Contains(err.Error(), "sk-SECRET-abc123") {
		t.Errorf("the error echoes the credential in the block: %v", err)
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

// TestParseRejectsUnterminatedTOMLBlock includes the rule that only the delimiter
// that opened a block can close it: a "---" line leaves a "+++" block open.
func TestParseRejectsUnterminatedTOMLBlock(t *testing.T) {
	for _, doc := range []string{
		"+++",
		"+++\n",
		"+++\ntitle = \"Hello\"\n",
		"+++\ntitle = \"Hello\"\n---\nbody\n",
	} {
		t.Run(strings.ReplaceAll(doc, "\n", `\n`), func(t *testing.T) {
			_, err := Parse([]byte(doc))
			if !errors.Is(err, core.ErrInvalid) {
				t.Fatalf("Parse error = %v; want one matching core.ErrInvalid", err)
			}
			if !strings.Contains(err.Error(), "line 1") {
				t.Errorf("Parse error = %v; want a message naming the line the block opened on", err)
			}
			if !strings.Contains(err.Error(), tomlDelimiter) {
				t.Errorf("Parse error = %v; want a message naming the %q delimiter", err, tomlDelimiter)
			}
		})
	}
}

// TestTOMLTopLevelKeyOrderIsRecoveredFromTheBlock covers what the serializer needs
// from the authored block: the top-level keys in source order, and that a re-encoded
// block puts the non-table keys first, because TOML forbids a top-level scalar after
// a table header.
//
// The second half is asserted against tomlKeyOrder rather than against the scanner,
// because that is now where table-ness is decided -- from the decoded values, which
// are the more correct source: after an edit a key authored as a table may no longer
// hold one, and the authored syntax would then be wrong about it.
func TestTOMLTopLevelKeyOrderIsRecoveredFromTheBlock(t *testing.T) {
	block := []byte("zebra = 1\napple = 2\n\n[author]\nname = \"Ada\"\nnested = { deep = true }\n\n[[links]]\nurl = \"one\"\n\n[[links]]\nurl = \"two\"\n")

	order := tomlTopLevelKeys(block)

	want := []string{"zebra", "apple", "author", "links"}
	if !slices.Equal(order, want) {
		t.Errorf("order = %q; want %q", order, want)
	}

	fields, err := (tomlCodec{}).decode(block)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := tomlKeyOrder(fields, order); !slices.Equal(got, want) {
		t.Errorf("tomlKeyOrder = %q; want %q", got, want)
	}
	// And a table key authored between two scalars still moves after them.
	if got := tomlKeyOrder(fields, []string{"author", "zebra", "links", "apple"}); !slices.Equal(got, want) {
		t.Errorf("tomlKeyOrder = %q; want %q: a table key must follow the last scalar", got, want)
	}

	if got := tomlTopLevelKeys([]byte("")); got != nil {
		t.Errorf("tomlTopLevelKeys of an empty block = %q; want none", got)
	}
}

// tomlKeyOrderCases is the scanner's specification: well-formed but awkward blocks,
// each with the exact top-level key order tomlTopLevelKeys must report for it.
//
// Every row asserts the whole ordered result rather than the presence of one key, so
// a case that starts reporting an extra key fails too. Three rows are load-bearing
// rather than decorative. "crlf" is here because codec.encode receives fm.Raw
// verbatim and Raw is CRLF for a Windows checkout. "key-shadowed-in-table" is the
// test for the rule that after the first table header there are no more top-level
// key-value lines. "array-of-arrays" is the test for carrying bracket depth across
// newlines: a line reading "[1, 2]," inside a multi-line array is not a table
// header, and depth is the only thing that tells them apart.
//
// No block may contain a line equal to "+++": the tests that route a fixture through
// Parse would see it close the block.
var tomlKeyOrderCases = []struct {
	name  string
	block string
	want  []string
}{
	// Comments and strings: a "#" is a comment everywhere except inside a string,
	// and a string ends where its own quoting rules say it does.
	{"comment-only", "# just a comment\n", nil},
	{"blank-lines-around", "\n\nzebra = 1\n\n\napple = 2\n\n", []string{"zebra", "apple"}},
	{"comment-between-keys", "zebra = 1\n# about apple\napple = 2\n", []string{"zebra", "apple"}},
	{"trailing-comment", "zebra = 1 # the zebra\napple = 2\n", []string{"zebra", "apple"}},
	{"hash-in-basic-string", "zebra = \"a # b\"\napple = 2\n", []string{"zebra", "apple"}},
	{"hash-in-literal-string", "zebra = 'a # b'\napple = 2\n", []string{"zebra", "apple"}},
	{"escaped-quote-in-basic-string", "zebra = \"a \\\" # b\"\napple = 2\n", []string{"zebra", "apple"}},
	// A literal string has no escapes, so 'C:\' ends at the quote.
	{"trailing-backslash-in-literal", "zebra = 'C:\\'\napple = 2\n", []string{"zebra", "apple"}},
	{"equals-in-string", "zebra = \"a = b\"\napple = 2\n", []string{"zebra", "apple"}},
	{"brackets-in-string", "zebra = \"[not a header]\"\napple = 2\n", []string{"zebra", "apple"}},

	// Multi-line strings. The first two each hold a "#", an "=", a "[not a header]"
	// line and a blank line, so a line inside one is proved not to be read as a key
	// or a header. Every row here is followed by a further top-level key, which is
	// what checks that the scanner found the end of the string.
	{"multiline-basic", "zebra = \"\"\"\n# not a comment\nkey = not a key\n\n[not a header]\n\"\"\"\napple = 2\n", []string{"zebra", "apple"}},
	{"multiline-literal", "zebra = '''\n# not a comment\nkey = not a key\n\n[not a header]\n'''\napple = 2\n", []string{"zebra", "apple"}},
	{"multiline-with-quotes", "zebra = \"\"\"a \"quoted\" word and \"\" two\"\"\"\napple = 2\n", []string{"zebra", "apple"}},
	// A closing """ may be followed by one or two more quotes that belong to the
	// content, so this value ends with a quote character.
	{"multiline-ending-four-quotes", "zebra = \"\"\"ends with a quote\"\"\"\"\napple = 2\n", []string{"zebra", "apple"}},
	{"multiline-escaped-delimiter", "zebra = \"\"\"a \\\"\\\"\\\" inside\"\"\"\napple = 2\n", []string{"zebra", "apple"}},

	// Keys. A quoted key is one name however many dots it holds; a dotted key names
	// a table, and only its first part is top level.
	{"quoted-key-with-dot", "\"a.b\" = 1\napple = 2\n", []string{"a.b", "apple"}},
	{"literal-quoted-key", "'a.b' = 1\napple = 2\n", []string{"a.b", "apple"}},
	{"dotted-key", "a.b = 1\napple = 2\n", []string{"a", "apple"}},
	{"dotted-key-with-spaces", "a . b = 1\napple = 2\n", []string{"a", "apple"}},
	{"dotted-key-quoted-tail", "a.\"b.c\" = 1\napple = 2\n", []string{"a", "apple"}},
	{"quoted-key-with-equals", "\"a = b\" = 1\napple = 2\n", []string{"a = b", "apple"}},
	{"empty-quoted-key", "\"\" = 1\napple = 2\n", []string{"", "apple"}},

	// Containers, which is where depth has to be tracked rather than lines counted.
	{"inline-table", "zebra = { a = 1, b = 2 }\napple = 2\n", []string{"zebra", "apple"}},
	{"nested-inline-table", "zebra = { a = { b = { c = 1 } } }\napple = 2\n", []string{"zebra", "apple"}},
	{"multiline-array", "zebra = [\n  1,\n  2,\n]\napple = 2\n", []string{"zebra", "apple"}},
	{"array-of-arrays", "zebra = [\n  [1, 2],\n  [3, 4],\n]\napple = 2\n", []string{"zebra", "apple"}},
	{"array-with-comment", "zebra = [\n  1, # one\n  2,\n]\napple = 2\n", []string{"zebra", "apple"}},
	{"array-with-bracket-string", "zebra = [\"[not a header]\"]\napple = 2\n", []string{"zebra", "apple"}},

	// Headers, which are the only thing that names a top-level key once one has
	// been seen.
	{"table-header", "zebra = 1\n\n[author]\nname = \"Ada\"\n", []string{"zebra", "author"}},
	{"key-shadowed-in-table", "zebra = 1\n\n[author]\napple = 2\n", []string{"zebra", "author"}},
	{"array-of-tables-repeated", "zebra = 1\n\n[[links]]\nurl = \"one\"\n\n[[links]]\nurl = \"two\"\n", []string{"zebra", "links"}},
	{"dotted-table-header", "zebra = 1\n\n[a.b]\nc = 1\n", []string{"zebra", "a"}},
	{"quoted-table-header", "zebra = 1\n\n[\"a.b\"]\nc = 1\n", []string{"zebra", "a.b"}},
	{"header-trailing-comment", "zebra = 1\n\n[author] # who\nname = \"Ada\"\n", []string{"zebra", "author"}},
	{"header-inner-spaces", "zebra = 1\n\n[ a . b ]\nc = 1\n", []string{"zebra", "a"}},
	{"header-with-bracket-in-name", "zebra = 1\n\n[\"a]b\"]\nc = 1\n", []string{"zebra", "a]b"}},
	{"subtable-after-table", "zebra = 1\n\n[author]\nname = \"Ada\"\n\n[author.address]\ncity = \"London\"\n", []string{"zebra", "author"}},

	// The shape of the block itself.
	{"empty", "", nil},
	{"plain-scalars", "zebra = 1\ntitle = \"Hello\"\napple = 2\n", []string{"zebra", "title", "apple"}},
	{"crlf", "zebra = 1\r\ntitle = \"Hello\"\r\napple = 2\r\n", []string{"zebra", "title", "apple"}},
	{"no-trailing-newline", "zebra = 1\napple = 2", []string{"zebra", "apple"}},
}

// assertTOMLKeySetMatchesTheDecoder checks the scanner against the one second
// opinion available: the set of top-level keys tomlCodec's own decode produces for
// the same block.
//
// The order cannot be checked this way -- reporting it is precisely what no stable
// TOML API does, which is why the scanner exists -- but the set can, and it is the
// property that matters: the scanner is right when it names exactly the keys
// orderedKeys will be asked about.
func assertTOMLKeySetMatchesTheDecoder(t *testing.T, block []byte) {
	t.Helper()

	fields, err := (tomlCodec{}).decode(block)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	scanned := slices.Sorted(slices.Values(tomlTopLevelKeys(block)))
	decoded := slices.Sorted(maps.Keys(fields))
	if !slices.Equal(scanned, decoded) {
		t.Errorf("scanned keys = %q; want the decoder's top-level keys %q", scanned, decoded)
	}
}

func TestTOMLTopLevelKeysAreRecoveredInAuthoredOrder(t *testing.T) {
	for _, tc := range tomlKeyOrderCases {
		t.Run(tc.name, func(t *testing.T) {
			block := []byte(tc.block)

			if got := tomlTopLevelKeys(block); !slices.Equal(got, tc.want) {
				t.Errorf("tomlTopLevelKeys = %q; want the authored order %q", got, tc.want)
			}
			assertTOMLKeySetMatchesTheDecoder(t, block)
		})
	}
}

// FuzzTOMLTopLevelKeysMatchTheDecodedFields holds the scanner to the same set
// equality over arbitrary bytes, and to the two things its precondition promises but
// does not prove: it terminates, and it does not panic.
//
// It earns its place because FuzzParseBytesRoundTrip does not reach this code at
// all. That target parses and serializes without editing anything, so every document
// it builds takes the passthrough path and returns Raw verbatim, without the encoder
// or the scanner ever running.
func FuzzTOMLTopLevelKeysMatchTheDecodedFields(f *testing.F) {
	for _, tc := range tomlKeyOrderCases {
		f.Add([]byte(tc.block))
	}

	f.Fuzz(func(t *testing.T, block []byte) {
		fields, err := (tomlCodec{}).decode(block)
		if err != nil {
			// Out of contract: the scanner is only ever handed a block the
			// decoder has already accepted. All that is asserted here is that
			// scanning one returns at all.
			tomlTopLevelKeys(block)
			return
		}
		scanned := slices.Sorted(slices.Values(tomlTopLevelKeys(block)))
		decoded := slices.Sorted(maps.Keys(fields))
		if !slices.Equal(scanned, decoded) {
			t.Errorf("scanned keys = %q; want the decoder's top-level keys %q", scanned, decoded)
		}
	})
}
