package core

import (
	"errors"
	"slices"
	"testing"
	"time"
)

func TestFrontMatterFormatValid(t *testing.T) {
	tests := []struct {
		format FrontMatterFormat
		want   bool
	}{
		{FrontMatterNone, true},
		{FrontMatterYAML, true},
		{FrontMatterTOML, true},
		{FrontMatterJSON, true},
		{FrontMatterFormat("YAML"), false},
		{FrontMatterFormat("xml"), false},
	}

	for _, tc := range tests {
		t.Run(string(tc.format), func(t *testing.T) {
			if got := tc.format.Valid(); got != tc.want {
				t.Errorf("FrontMatterFormat(%q).Valid() = %t; want %t", string(tc.format), got, tc.want)
			}
			if got := tc.format.String(); got != string(tc.format) {
				t.Errorf("String() = %q; want %q", got, string(tc.format))
			}
		})
	}
}

func TestFrontMatterZeroValueIsEmpty(t *testing.T) {
	var fm FrontMatter

	if !fm.IsZero() {
		t.Errorf("IsZero() = false; want true")
	}
	if v, ok := fm.Lookup("title"); v != nil || ok {
		t.Errorf("Lookup on the zero value = (%v, %t); want (nil, false)", v, ok)
	}

	nonZero := []struct {
		name string
		fm   FrontMatter
	}{
		{"format", FrontMatter{Format: FrontMatterYAML}},
		{"raw", FrontMatter{Raw: []byte("title: Hello\n")}},
		{"fields", FrontMatter{Fields: map[string]any{"title": "Hello"}}},
	}
	for _, tc := range nonZero {
		t.Run(tc.name, func(t *testing.T) {
			if tc.fm.IsZero() {
				t.Errorf("IsZero() = true; want false when %s is set", tc.name)
			}
		})
	}
}

func TestFrontMatterLookupAndTypedAccessors(t *testing.T) {
	fm := FrontMatter{
		Format: FrontMatterYAML,
		Fields: map[string]any{
			"title":   "Hello",
			"draft":   true,
			"tags":    []any{"go", "cms"},
			"weight":  3,
			"authors": []string{"ada", "grace"},
			"mixed":   []any{"go", 42},
		},
	}

	if v, ok := fm.Lookup("title"); v != "Hello" || !ok {
		t.Errorf(`Lookup("title") = (%v, %t); want ("Hello", true)`, v, ok)
	}
	if v, ok := fm.Lookup("missing"); v != nil || ok {
		t.Errorf(`Lookup("missing") = (%v, %t); want (nil, false)`, v, ok)
	}

	if got, ok := fm.Text("title"); got != "Hello" || !ok {
		t.Errorf(`Text("title") = (%q, %t); want ("Hello", true)`, got, ok)
	}
	if got, ok := fm.Text("draft"); got != "" || ok {
		t.Errorf(`Text("draft") = (%q, %t); want ("", false): no coercion across types`, got, ok)
	}
	if got, ok := fm.Text("weight"); got != "" || ok {
		t.Errorf(`Text("weight") = (%q, %t); want ("", false)`, got, ok)
	}

	if got, ok := fm.Bool("draft"); !got || !ok {
		t.Errorf(`Bool("draft") = (%t, %t); want (true, true)`, got, ok)
	}
	if got, ok := fm.Bool("title"); got || ok {
		t.Errorf(`Bool("title") = (%t, %t); want (false, false)`, got, ok)
	}

	if got, ok := fm.Strings("tags"); !ok || !slices.Equal(got, []string{"go", "cms"}) {
		t.Errorf(`Strings("tags") = (%v, %t); want ([go cms], true)`, got, ok)
	}
	if got, ok := fm.Strings("authors"); !ok || !slices.Equal(got, []string{"ada", "grace"}) {
		t.Errorf(`Strings("authors") = (%v, %t); want ([ada grace], true)`, got, ok)
	}
	if got, ok := fm.Strings("title"); got != nil || ok {
		t.Errorf(`Strings("title") = (%v, %t); want (nil, false)`, got, ok)
	}
	if got, ok := fm.Strings("mixed"); got != nil || ok {
		t.Errorf(`Strings("mixed") = (%v, %t); want (nil, false)`, got, ok)
	}

	t.Run("missing key", func(t *testing.T) {
		if _, ok := fm.Text("missing"); ok {
			t.Errorf(`Text("missing") ok = true; want false`)
		}
		if _, ok := fm.Bool("missing"); ok {
			t.Errorf(`Bool("missing") ok = true; want false`)
		}
		if _, ok := fm.Time("missing"); ok {
			t.Errorf(`Time("missing") ok = true; want false`)
		}
		if _, ok := fm.Strings("missing"); ok {
			t.Errorf(`Strings("missing") ok = true; want false`)
		}
	})

	t.Run("nil fields", func(t *testing.T) {
		var empty FrontMatter
		if _, ok := empty.Text("title"); ok {
			t.Errorf("Text on a nil Fields map ok = true; want false")
		}
		if _, ok := empty.Bool("draft"); ok {
			t.Errorf("Bool on a nil Fields map ok = true; want false")
		}
		if _, ok := empty.Time("date"); ok {
			t.Errorf("Time on a nil Fields map ok = true; want false")
		}
		if _, ok := empty.Strings("tags"); ok {
			t.Errorf("Strings on a nil Fields map ok = true; want false")
		}
	})
}

func TestFrontMatterTimeAcceptsTimeAndCommonStrings(t *testing.T) {
	want := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		value any
		want  time.Time
		ok    bool
	}{
		{"time.Time", want, want, true},
		{"rfc3339", "2026-08-30T12:00:00Z", want, true},
		{"date only", "2026-08-30", time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC), true},
		{"not a date", "not a date", time.Time{}, false},
		{"int", 42, time.Time{}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fm := FrontMatter{Format: FrontMatterYAML, Fields: map[string]any{"date": tc.value}}
			got, ok := fm.Time("date")
			if ok != tc.ok {
				t.Fatalf(`Time("date") ok = %t; want %t`, ok, tc.ok)
			}
			if !got.Equal(tc.want) {
				t.Errorf(`Time("date") = %v; want %v`, got, tc.want)
			}
		})
	}
}

func TestFrontMatterCloneIsIndependent(t *testing.T) {
	original := FrontMatter{
		Format: FrontMatterYAML,
		Raw:    []byte("title: Hello\n"),
		Fields: map[string]any{"title": "Hello"},
	}

	clone := original.Clone()
	clone.Fields["title"] = "Changed"
	clone.Fields["added"] = true
	clone.Raw[0] = 'X'

	if got, _ := original.Text("title"); got != "Hello" {
		t.Errorf("original title = %q; want %q", got, "Hello")
	}
	if _, ok := original.Lookup("added"); ok {
		t.Errorf("original gained the key added; want the maps independent")
	}
	if string(original.Raw) != "title: Hello\n" {
		t.Errorf("original Raw = %q; want %q", original.Raw, "title: Hello\n")
	}

	original.Fields["title"] = "Reverted"
	if got, _ := clone.Text("title"); got != "Changed" {
		t.Errorf("clone title = %q; want %q: independence must hold both ways", got, "Changed")
	}

	var zero FrontMatter
	if !zero.Clone().IsZero() {
		t.Errorf("cloning the zero value did not yield a zero FrontMatter")
	}
}

func TestFrontMatterValidate(t *testing.T) {
	tests := []struct {
		name string
		fm   FrontMatter
		ok   bool
	}{
		{"zero", FrontMatter{}, true},
		{"yaml with fields", FrontMatter{Format: FrontMatterYAML, Raw: []byte("title: Hello\n"), Fields: map[string]any{"title": "Hello"}}, true},
		{"unknown format", FrontMatter{Format: FrontMatterFormat("xml"), Raw: []byte("<x/>")}, false},
		{"none with fields", FrontMatter{Fields: map[string]any{"title": "Hello"}}, false},
		{"none with raw", FrontMatter{Raw: []byte("title: Hello\n")}, false},
		{"empty key", FrontMatter{Format: FrontMatterYAML, Fields: map[string]any{"": "Hello"}}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.fm.Validate()
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
