package markdown

import (
	"bytes"
	"testing"
)

// FuzzParseBytesRoundTrip asserts the phase's central property over arbitrary
// bytes: Parse either rejects a document, or the Document it returns serializes back
// to exactly the input. It also asserts that Parse never panics, whatever it is
// handed.
//
// The seed corpus is the round-trip corpus plus a handful of fragments chosen to sit
// on the slot boundaries -- a bare delimiter, a delimiter with no terminator, a lone
// opening brace, a mark on its own -- because those are where an off-by-one in the
// five-slot split would hide.
func FuzzParseBytesRoundTrip(f *testing.F) {
	for _, tc := range corpus {
		f.Add([]byte(tc.doc))
	}
	for _, fragment := range []string{
		"---",
		"---\n",
		"---\n---",
		"---\r\n---\r\n",
		"---  \n---",
		"+++",
		"+++\n+++",
		"{",
		"{}",
		"{} ",
		"{}\n",
		"\ufeff",
		"\ufeff---\n---\n",
		"\ufeff{}",
		"\x00",
		"\xff",
		"---\n\x00\n---\n",
		"...\n",
		"\n---\n---\n",
	} {
		f.Add([]byte(fragment))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		doc, err := Parse(data)
		if err != nil {
			// Rejecting a document is a valid outcome; emitting a different one
			// is not.
			return
		}

		out, err := doc.Bytes()
		if err != nil {
			t.Fatalf("Parse accepted a document that Bytes then refused: %v", err)
		}
		if !bytes.Equal(out, data) {
			t.Errorf("round trip changed the document: %s", byteDiff(data, out))
		}
	})
}
