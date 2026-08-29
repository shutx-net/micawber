package core

import (
	"errors"
	"strings"
	"testing"
)

// rejectCase is a single invalid input and the label it is reported under.
type rejectCase struct {
	name string
	in   string
}

// assertContentPathRejected asserts that every case fails with an ErrInvalid
// error and yields the zero ContentPath.
func assertContentPathRejected(t *testing.T, cases []rejectCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewContentPath(tc.in)
			if err == nil {
				t.Fatalf("NewContentPath(%q) = %q, nil; want an error", tc.in, got)
			}
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("NewContentPath(%q) error = %v; want errors.Is(err, ErrInvalid)", tc.in, err)
			}
			if got != (ContentPath{}) {
				t.Errorf("NewContentPath(%q) = %#v; want the zero ContentPath", tc.in, got)
			}
		})
	}
}

func TestNewContentPathAcceptsValidPaths(t *testing.T) {
	valid := []string{
		"a.md",
		"posts/hello-world.md",
		"posts/2026/01/a.markdown",
		"blog/entry.md",
		"記事/こんにちは.md",
		"posts/.hidden.md",
		"a.txt",
	}

	for _, in := range valid {
		t.Run(in, func(t *testing.T) {
			p, err := NewContentPath(in)
			if err != nil {
				t.Fatalf("NewContentPath(%q) error = %v; want nil", in, err)
			}
			if p.String() != in {
				t.Errorf("String() = %q; want %q (input must not be normalized)", p.String(), in)
			}
			if p.IsZero() {
				t.Errorf("IsZero() = true; want false for %q", in)
			}
		})
	}
}

func TestNewContentPathRejectsTraversalAndAbsolutePaths(t *testing.T) {
	assertContentPathRejected(t, []rejectCase{
		{"empty", ""},
		{"absolute", "/abs.md"},
		{"traversal", "posts/../../etc/passwd"},
		{"dotdot", ".."},
		{"leading dot segment", "./a.md"},
		{"interior dot segment", "a/./b.md"},
		{"empty segment", "a//b.md"},
		{"trailing slash", "posts/"},
		{"windows drive", "C:/a.md"},
	})
}

func TestNewContentPathRejectsUnsafeBytes(t *testing.T) {
	assertContentPathRejected(t, []rejectCase{
		{"backslash", `posts\a.md`},
		{"nul", "a\x00.md"},
		{"newline", "a\nb.md"},
		{"delete", "a\x7f.md"},
		{"invalid utf-8", "a\xff.md"},
	})
}

func TestNewContentPathRejectsGitSegment(t *testing.T) {
	assertContentPathRejected(t, []rejectCase{
		{"git at root", ".git/config"},
		{"git nested", "posts/.git/x.md"},
		{"git uppercase", ".GIT/config"},
		{"git mixed case", "posts/.Git/x.md"},
		{"git titlecase nested", "posts/.giT/x.md"},
	})
}

func TestNewContentPathRejectsOversizedPaths(t *testing.T) {
	assertContentPathRejected(t, []rejectCase{
		{"oversized segment", strings.Repeat("a", 300) + ".md"},
		{"oversized path", strings.Repeat("dir/", 1500) + "a.md"},
	})
}

func TestContentPathAccessors(t *testing.T) {
	tests := []struct {
		in         string
		dir        string
		base       string
		ext        string
		isMarkdown bool
	}{
		{"posts/hello.md", "posts", "hello.md", ".md", true},
		{"a.md", ".", "a.md", ".md", true},
		{"posts/2026/01/a.markdown", "posts/2026/01", "a.markdown", ".markdown", true},
		{"notes/readme.MD", "notes", "readme.MD", ".MD", true},
		{"data/x.json", "data", "x.json", ".json", false},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			p, err := NewContentPath(tc.in)
			if err != nil {
				t.Fatalf("NewContentPath(%q) error = %v; want nil", tc.in, err)
			}
			if got := p.Dir(); got != tc.dir {
				t.Errorf("Dir() = %q; want %q", got, tc.dir)
			}
			if got := p.Base(); got != tc.base {
				t.Errorf("Base() = %q; want %q", got, tc.base)
			}
			if got := p.Ext(); got != tc.ext {
				t.Errorf("Ext() = %q; want %q", got, tc.ext)
			}
			if got := p.IsMarkdown(); got != tc.isMarkdown {
				t.Errorf("IsMarkdown() = %t; want %t", got, tc.isMarkdown)
			}
		})
	}
}

func TestContentPathZeroValue(t *testing.T) {
	var p ContentPath

	if !p.IsZero() {
		t.Errorf("IsZero() = false; want true")
	}
	if got := p.String(); got != "" {
		t.Errorf("String() = %q; want %q", got, "")
	}
	if p.IsMarkdown() {
		t.Errorf("IsMarkdown() = true; want false")
	}
}

func TestContentPathTextRoundTrip(t *testing.T) {
	p, err := NewContentPath("posts/hello.md")
	if err != nil {
		t.Fatalf("NewContentPath error = %v; want nil", err)
	}

	b, err := p.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText error = %v; want nil", err)
	}
	if string(b) != "posts/hello.md" {
		t.Errorf("MarshalText = %q; want %q", b, "posts/hello.md")
	}

	var got ContentPath
	if err := got.UnmarshalText(b); err != nil {
		t.Fatalf("UnmarshalText error = %v; want nil", err)
	}
	if got != p {
		t.Errorf("round trip = %#v; want %#v", got, p)
	}

	var bad ContentPath
	err = bad.UnmarshalText([]byte("../x"))
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("UnmarshalText(%q) error = %v; want errors.Is(err, ErrInvalid)", "../x", err)
	}
	if !bad.IsZero() {
		t.Errorf("UnmarshalText left %#v; want the receiver zero", bad)
	}
}
