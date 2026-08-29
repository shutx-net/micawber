package core

import (
	"errors"
	"testing"
	"time"
)

func TestRevisionIsZero(t *testing.T) {
	if !Revision("").IsZero() {
		t.Errorf(`Revision("").IsZero() = false; want true`)
	}
	if Revision("abc123").IsZero() {
		t.Errorf(`Revision("abc123").IsZero() = true; want false`)
	}
}

func TestContentValidate(t *testing.T) {
	path := mustContentPath(t, "posts/hello.md")

	tests := []struct {
		name string
		c    Content
		ok   bool
	}{
		{
			name: "full",
			c: Content{
				Path:        path,
				FrontMatter: FrontMatter{Format: FrontMatterYAML, Raw: []byte("title: Hello\n"), Fields: map[string]any{"title": "Hello"}},
				Body:        []byte("# Hello\n"),
				Revision:    Revision("abc123"),
			},
			ok: true,
		},
		{
			name: "empty body and no front matter",
			c:    Content{Path: path},
			ok:   true,
		},
		{
			name: "zero path",
			c:    Content{Body: []byte("# Hello\n")},
			ok:   false,
		},
		{
			name: "unknown front matter format",
			c:    Content{Path: path, FrontMatter: FrontMatter{Format: FrontMatterFormat("xml"), Raw: []byte("<x/>")}},
			ok:   false,
		},
		{
			name: "front matter fields without a format",
			c:    Content{Path: path, FrontMatter: FrontMatter{Fields: map[string]any{"title": "Hello"}}},
			ok:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.c.Validate()
			if tc.ok {
				if err != nil {
					t.Fatalf("Validate() error = %v; want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil; want an error")
			}
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("Validate() error = %v; want errors.Is(err, ErrInvalid)", err)
			}
		})
	}
}

func TestContentCloneIsIndependent(t *testing.T) {
	original := Content{
		Path:        mustContentPath(t, "posts/hello.md"),
		FrontMatter: FrontMatter{Format: FrontMatterYAML, Raw: []byte("title: Hello\n"), Fields: map[string]any{"title": "Hello"}},
		Body:        []byte("# Hello\n"),
		Revision:    Revision("abc123"),
	}

	clone := original.Clone()
	if clone.Path != original.Path || clone.Revision != original.Revision {
		t.Fatalf("Clone() = %#v; want the path and revision preserved", clone)
	}

	clone.Body[0] = 'X'
	clone.FrontMatter.Fields["title"] = "Changed"
	if string(original.Body) != "# Hello\n" {
		t.Errorf("original Body = %q; want %q", original.Body, "# Hello\n")
	}
	if got, _ := original.FrontMatter.Text("title"); got != "Hello" {
		t.Errorf("original title = %q; want %q", got, "Hello")
	}

	original.Body[0] = 'Y'
	original.FrontMatter.Fields["title"] = "Reverted"
	if string(clone.Body) != "X Hello\n" {
		t.Errorf("clone Body = %q; want %q: independence must hold both ways", clone.Body, "X Hello\n")
	}
	if got, _ := clone.FrontMatter.Text("title"); got != "Changed" {
		t.Errorf("clone title = %q; want %q: independence must hold both ways", got, "Changed")
	}
}

func TestAuthorValidate(t *testing.T) {
	tests := []struct {
		name   string
		author Author
		ok     bool
	}{
		{"name and email", Author{Name: "Ada", Email: "ada@example.com"}, true},
		{"name only", Author{Name: "Ada"}, true},
		{"empty name", Author{Email: "ada@example.com"}, false},
		{"whitespace name", Author{Name: "   \t", Email: "ada@example.com"}, false},
		{"newline in name", Author{Name: "Ada\nLovelace", Email: "ada@example.com"}, false},
		{"carriage return in name", Author{Name: "Ada\rLovelace"}, false},
		{"nul in name", Author{Name: "Ada\x00"}, false},
		{"newline in email", Author{Name: "Ada", Email: "ada@example.com\nx"}, false},
		{"carriage return in email", Author{Name: "Ada", Email: "ada@example.com\r"}, false},
		{"nul in email", Author{Name: "Ada", Email: "ada@example.com\x00"}, false},
		{"email without at sign", Author{Name: "Ada", Email: "ada.example.com"}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.author.Validate()
			if tc.ok {
				if err != nil {
					t.Fatalf("Validate() error = %v; want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil; want an error")
			}
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("Validate() error = %v; want errors.Is(err, ErrInvalid)", err)
			}
		})
	}

	t.Run("string", func(t *testing.T) {
		if got := (Author{Name: "Ada", Email: "ada@example.com"}).String(); got != "Ada <ada@example.com>" {
			t.Errorf("String() = %q; want %q", got, "Ada <ada@example.com>")
		}
		if got := (Author{Name: "Ada"}).String(); got != "Ada" {
			t.Errorf("String() = %q; want %q", got, "Ada")
		}
	})
}

func TestChangeValidate(t *testing.T) {
	author := Author{Name: "Ada", Email: "ada@example.com"}

	tests := []struct {
		name   string
		change Change
		ok     bool
	}{
		{"message and author", Change{Message: "Add a post", Author: author, Time: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)}, true},
		{"zero time", Change{Message: "Add a post", Author: author}, true},
		{"multi-line message", Change{Message: "Add a post\n\nWith a body paragraph.\n", Author: author}, true},
		{"zero change", Change{}, false},
		{"empty message", Change{Author: author}, false},
		{"whitespace message", Change{Message: " \n\t ", Author: author}, false},
		{"nul in message", Change{Message: "Add a post\x00", Author: author}, false},
		{"invalid author", Change{Message: "Add a post", Author: Author{Name: "Ada\n"}}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.change.Validate()
			if tc.ok {
				if err != nil {
					t.Fatalf("Validate() error = %v; want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil; want an error")
			}
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("Validate() error = %v; want errors.Is(err, ErrInvalid)", err)
			}
		})
	}
}
