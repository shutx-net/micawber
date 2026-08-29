package markdown

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shutx-net/micawber/core"
)

// TestParseRejectsAnOverlongFrontMatterBlock covers the cost that
// MaxFrontMatterBytes does not: yaml/v3's decode into a map is quadratic in the
// number of entries, so a block far inside the byte limit can still take
// minutes. Measured before this guard existed: 40 KB of duplicate keys, 3.8% of
// MaxFrontMatterBytes, took 12.4 seconds, and a full-size block never finished.
func TestParseRejectsAnOverlongFrontMatterBlock(t *testing.T) {
	var atTheLimit strings.Builder
	atTheLimit.WriteString("---\n")
	for i := range MaxFrontMatterLines {
		fmt.Fprintf(&atTheLimit, "key%06d: value\n", i)
	}
	atTheLimit.WriteString("---\nbody\n")

	doc, err := Parse([]byte(atTheLimit.String()))
	if err != nil {
		t.Fatalf("Parse of a block of exactly MaxFrontMatterLines lines: %v", err)
	}
	if got := len(doc.Content.FrontMatter.Fields); got != MaxFrontMatterLines {
		t.Errorf("decoded %d fields; want %d", got, MaxFrontMatterLines)
	}

	overTheLimit := "---\n" + strings.Repeat("k: 1\n", MaxFrontMatterLines+1) + "---\nbody\n"

	// The bound is only worth having if it fires before the decoder does. A
	// block this shape took multiple seconds to be rejected by yaml/v3 itself,
	// so a generous ceiling still tells the two apart.
	start := time.Now()
	if _, err := Parse([]byte(overTheLimit)); err == nil {
		t.Error("Parse of a block over MaxFrontMatterLines = nil; want an error")
	} else if !errors.Is(err, core.ErrInvalid) {
		t.Errorf("Parse error = %v; want errors.Is(err, core.ErrInvalid)", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("rejecting an oversized block took %s; the guard must run before the decoder", elapsed)
	}
}

// TestParseRejectsUnterminatedBlock pins the strong-signal rule: a "---" first line
// means front matter and nothing else, so a block that is never closed is a damaged
// file to report rather than prose to present.
func TestParseRejectsUnterminatedBlock(t *testing.T) {
	for _, doc := range []string{
		"---",
		"---\n",
		"---\ntitle: Hello\n",
		"---\ntitle: Hello\nbody without a closing delimiter\n",
		"\ufeff---\ntitle: Hello\n",
		"---\n...\ntitle: Hello\n",
	} {
		t.Run(strings.ReplaceAll(doc, "\n", `\n`), func(t *testing.T) {
			_, err := Parse([]byte(doc))
			if !errors.Is(err, core.ErrInvalid) {
				t.Fatalf("Parse error = %v; want one matching core.ErrInvalid", err)
			}
			if !strings.Contains(err.Error(), "line 1") {
				t.Errorf("Parse error = %v; want a message naming the line the block opened on", err)
			}
		})
	}
}

func TestParseRejectsNULInsideTheFrontMatterBlock(t *testing.T) {
	for _, doc := range []string{
		"---\ntitle: Hel\x00lo\n---\nbody\n",
		"---\n\x00\n---\n",
	} {
		t.Run(strings.ReplaceAll(doc, "\n", `\n`), func(t *testing.T) {
			_, err := Parse([]byte(doc))
			if !errors.Is(err, core.ErrInvalid) {
				t.Fatalf("Parse error = %v; want one matching core.ErrInvalid", err)
			}
			if !strings.Contains(err.Error(), "NUL") {
				t.Errorf("Parse error = %v; want a message naming the NUL byte", err)
			}
		})
	}
}

// TestParseRejectsAnOversizedFrontMatterBlock asserts the scan gives up at
// MaxFrontMatterBytes rather than reading on: the closing delimiter here exists, one
// byte past the limit, and must not be found.
func TestParseRejectsAnOversizedFrontMatterBlock(t *testing.T) {
	fill := func(n int) string { return "k: " + strings.Repeat("a", n-4) + "\n" }

	atTheLimit := "---\n" + fill(MaxFrontMatterBytes) + "---\nbody\n"
	doc, err := Parse([]byte(atTheLimit))
	if err != nil {
		t.Fatalf("Parse of a block of exactly MaxFrontMatterBytes: %v", err)
	}
	if len(doc.Content.FrontMatter.Raw) != MaxFrontMatterBytes {
		t.Errorf("Raw = %d bytes; want %d", len(doc.Content.FrontMatter.Raw), MaxFrontMatterBytes)
	}

	overTheLimit := "---\n" + fill(MaxFrontMatterBytes+1) + "---\nbody\n"
	_, err = Parse([]byte(overTheLimit))
	if !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Parse of an oversized block: error = %v; want one matching core.ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "line 1") {
		t.Errorf("Parse error = %v; want a message naming the line the block opened on", err)
	}
}

func TestParseTreatsADelimiterLineInTheBodyAsBody(t *testing.T) {
	in := []byte("---\ntitle: Hello\n---\nIntro.\n\n---\n\nMore prose.\n+++\nnot toml either\n")

	doc, err := Parse(in)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := string(doc.Content.FrontMatter.Raw); got != "title: Hello\n" {
		t.Errorf("Raw = %q; want %q: only the first matching line closes the block", got, "title: Hello\n")
	}
	if want := "Intro.\n\n---\n\nMore prose.\n+++\nnot toml either\n"; string(doc.Content.Body) != want {
		t.Errorf("Body = %q; want %q", doc.Content.Body, want)
	}

	// And a document whose body opens with a delimiter that never closes is still
	// just a body, because the first line decided the question.
	in = []byte("Intro.\n---\nnot front matter\n")
	doc, err = Parse(in)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !bytes.Equal(doc.Content.Body, in) {
		t.Errorf("Body is not the whole document: %s", byteDiff(in, doc.Content.Body))
	}
}

// TestParseErrorsDoNotEchoDocumentBytes is a security assertion, not a style one: a
// front-matter block is exactly where a user may have put an API token, and these
// errors are meant to reach a log and an API client.
func TestParseErrorsDoNotEchoDocumentBytes(t *testing.T) {
	const token = "sekrit-token"

	tests := []struct {
		name string
		doc  string
	}{
		{"unterminated", "---\nsecret: " + token + "\n"},
		{"nul in the block", "---\nsecret: " + token + "\x00\n---\nbody\n"},
		{"malformed", "---\nsecret: [" + token + "\n---\nbody\n"},
		{"not a mapping", "---\n- " + token + "\n---\nbody\n"},
		{"duplicate key", "---\nsecret: " + token + "\nsecret: " + token + "\n---\nbody\n"},
		{"oversized", "---\nsecret: " + token + "\n" + strings.Repeat("x: y\n", MaxFrontMatterBytes/4) + "---\nbody\n"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.doc))
			if err == nil {
				t.Fatalf("Parse succeeded; want an error")
			}

			message := err.Error()
			if strings.Contains(message, token) {
				t.Errorf("the error echoes the block's contents: %v", err)
			}
			// Nothing else from the block may appear either, delimiter lines aside.
			for _, line := range strings.Split(tc.doc, "\n") {
				line = strings.TrimSpace(line)
				if line == "" || line == yamlDelimiter {
					continue
				}
				if strings.Contains(message, line) {
					t.Errorf("the error echoes a line of the document: %v", err)
				}
			}
		})
	}
}
