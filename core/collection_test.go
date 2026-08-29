package core

import (
	"errors"
	"strings"
	"testing"
)

// mustContentPath builds a ContentPath or fails the test.
func mustContentPath(t *testing.T, s string) ContentPath {
	t.Helper()
	p, err := NewContentPath(s)
	if err != nil {
		t.Fatalf("NewContentPath(%q) error = %v; want nil", s, err)
	}
	return p
}

// mustCollection builds a Collection or fails the test.
func mustCollection(t *testing.T, s string) Collection {
	t.Helper()
	c, err := NewCollection(s)
	if err != nil {
		t.Fatalf("NewCollection(%q) error = %v; want nil", s, err)
	}
	return c
}

func TestNewCollectionAcceptsValidNames(t *testing.T) {
	for _, in := range []string{"posts", "blog/posts", "docs/ja"} {
		t.Run(in, func(t *testing.T) {
			c := mustCollection(t, in)
			if got := c.String(); got != in {
				t.Errorf("String() = %q; want %q", got, in)
			}
			if c.IsRoot() {
				t.Errorf("IsRoot() = true; want false for %q", in)
			}
		})
	}
}

func TestNewCollectionRejectsInvalidNames(t *testing.T) {
	cases := []rejectCase{
		{"empty", ""},
		{"absolute", "/posts"},
		{"trailing slash", "posts/"},
		{"leading traversal", "../posts"},
		{"trailing traversal", "posts/.."},
		{"git", ".git"},
		{"nul", "posts\x00"},
		{"oversized", strings.Repeat("dir/", 1500) + "a"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewCollection(tc.in)
			if err == nil {
				t.Fatalf("NewCollection(%q) = %q, nil; want an error", tc.in, got)
			}
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("NewCollection(%q) error = %v; want errors.Is(err, ErrInvalid)", tc.in, err)
			}
			if got != (Collection{}) {
				t.Errorf("NewCollection(%q) = %#v; want the zero Collection", tc.in, got)
			}
		})
	}
}

func TestCollectionZeroValueIsRoot(t *testing.T) {
	var root Collection

	if !root.IsRoot() {
		t.Errorf("IsRoot() = false; want true")
	}
	if got := root.String(); got != "." {
		t.Errorf("String() = %q; want %q", got, ".")
	}
	for _, in := range []string{"a.md", "posts/a.md", "x/y/z.md"} {
		if !root.Contains(mustContentPath(t, in)) {
			t.Errorf("root.Contains(%q) = false; want true", in)
		}
	}
	if root.Contains(ContentPath{}) {
		t.Errorf("root.Contains(zero ContentPath) = true; want false")
	}
}

func TestCollectionContains(t *testing.T) {
	posts := mustCollection(t, "posts")

	tests := []struct {
		path string
		want bool
	}{
		{"posts/a.md", true},
		{"posts/2026/a.md", true},
		{"postsx/a.md", false},
		{"a.md", false},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			if got := posts.Contains(mustContentPath(t, tc.path)); got != tc.want {
				t.Errorf("Contains(%q) = %t; want %t", tc.path, got, tc.want)
			}
		})
	}

	if posts.Contains(ContentPath{}) {
		t.Errorf("Contains(zero ContentPath) = true; want false")
	}
}

func TestCollectionJoinRejectsEscape(t *testing.T) {
	posts := mustCollection(t, "posts")

	got, err := posts.Join("hello.md")
	if err != nil {
		t.Fatalf("Join(%q) error = %v; want nil", "hello.md", err)
	}
	if got.String() != "posts/hello.md" {
		t.Errorf("Join(%q) = %q; want %q", "hello.md", got, "posts/hello.md")
	}

	var root Collection
	got, err = root.Join("hello.md")
	if err != nil {
		t.Fatalf("root.Join(%q) error = %v; want nil", "hello.md", err)
	}
	if got.String() != "hello.md" {
		t.Errorf("root.Join(%q) = %q; want %q", "hello.md", got, "hello.md")
	}

	escapes := []rejectCase{
		{"traversal", "../secrets.md"},
		{"absolute", "/abs.md"},
		{"empty", ""},
		{"interior traversal", "a/../../b.md"},
		{"git", ".git/config"},
	}
	for _, tc := range escapes {
		t.Run(tc.name, func(t *testing.T) {
			got, err := posts.Join(tc.in)
			if err == nil {
				t.Fatalf("Join(%q) = %q, nil; want an error", tc.in, got)
			}
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("Join(%q) error = %v; want errors.Is(err, ErrInvalid)", tc.in, err)
			}
			if got != (ContentPath{}) {
				t.Errorf("Join(%q) = %#v; want the zero ContentPath", tc.in, got)
			}
		})
	}
}

func TestCollectionRel(t *testing.T) {
	posts := mustCollection(t, "posts")

	if got, ok := posts.Rel(mustContentPath(t, "posts/a.md")); got != "a.md" || !ok {
		t.Errorf(`Rel("posts/a.md") = (%q, %t); want ("a.md", true)`, got, ok)
	}
	if got, ok := posts.Rel(mustContentPath(t, "posts/2026/a.md")); got != "2026/a.md" || !ok {
		t.Errorf(`Rel("posts/2026/a.md") = (%q, %t); want ("2026/a.md", true)`, got, ok)
	}
	if got, ok := posts.Rel(mustContentPath(t, "other/a.md")); got != "" || ok {
		t.Errorf(`Rel("other/a.md") = (%q, %t); want ("", false)`, got, ok)
	}

	var root Collection
	if got, ok := root.Rel(mustContentPath(t, "posts/a.md")); got != "posts/a.md" || !ok {
		t.Errorf(`root.Rel("posts/a.md") = (%q, %t); want ("posts/a.md", true)`, got, ok)
	}
}

func TestCollectionTextRoundTrip(t *testing.T) {
	c := mustCollection(t, "blog/posts")

	b, err := c.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText error = %v; want nil", err)
	}
	if string(b) != "blog/posts" {
		t.Errorf("MarshalText = %q; want %q", b, "blog/posts")
	}

	var got Collection
	if err := got.UnmarshalText(b); err != nil {
		t.Fatalf("UnmarshalText error = %v; want nil", err)
	}
	if got != c {
		t.Errorf("round trip = %#v; want %#v", got, c)
	}

	var root Collection
	b, err = root.MarshalText()
	if err != nil {
		t.Fatalf("root MarshalText error = %v; want nil", err)
	}
	if string(b) != "." {
		t.Errorf("root MarshalText = %q; want %q", b, ".")
	}
	var gotRoot Collection
	if err := gotRoot.UnmarshalText(b); err != nil {
		t.Fatalf("root UnmarshalText error = %v; want nil", err)
	}
	if gotRoot != (Collection{}) {
		t.Errorf("root round trip = %#v; want the zero Collection", gotRoot)
	}

	var bad Collection
	err = bad.UnmarshalText([]byte("../x"))
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("UnmarshalText(%q) error = %v; want errors.Is(err, ErrInvalid)", "../x", err)
	}
	if !bad.IsRoot() {
		t.Errorf("UnmarshalText left %#v; want the receiver unchanged", bad)
	}
}
