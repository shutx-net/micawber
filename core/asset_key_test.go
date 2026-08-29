package core

import (
	"errors"
	"strings"
	"testing"
)

// assertAssetKeyRejected asserts that every case fails with an ErrInvalid
// error and yields the zero AssetKey.
func assertAssetKeyRejected(t *testing.T, cases []rejectCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewAssetKey(tc.in)
			if err == nil {
				t.Fatalf("NewAssetKey(%q) = %q, nil; want an error", tc.in, got)
			}
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("NewAssetKey(%q) error = %v; want errors.Is(err, ErrInvalid)", tc.in, err)
			}
			if got != (AssetKey{}) {
				t.Errorf("NewAssetKey(%q) = %#v; want the zero AssetKey", tc.in, got)
			}
		})
	}
}

func TestNewAssetKeyAcceptsValidKeys(t *testing.T) {
	valid := []string{
		"logo.png",
		"uploads/2026/08/photo.jpg",
		"a/b/c/d.bin",
		"画像/ロゴ.png",
		// Assets are not stored in the repository, so the ".git" rule that
		// guards content paths deliberately does not apply here.
		".git/x.png",
	}

	for _, in := range valid {
		t.Run(in, func(t *testing.T) {
			k, err := NewAssetKey(in)
			if err != nil {
				t.Fatalf("NewAssetKey(%q) error = %v; want nil", in, err)
			}
			if k.String() != in {
				t.Errorf("String() = %q; want %q", k.String(), in)
			}
			if k.IsZero() {
				t.Errorf("IsZero() = true; want false for %q", in)
			}
		})
	}
}

func TestNewAssetKeyRejectsTraversalAndAbsoluteKeys(t *testing.T) {
	assertAssetKeyRejected(t, []rejectCase{
		{"empty", ""},
		{"absolute", "/logo.png"},
		{"trailing slash", "uploads/"},
		{"empty segment", "a//b.png"},
		{"traversal", "a/../b.png"},
		{"dotdot", ".."},
	})
}

func TestNewAssetKeyRejectsUnsafeBytes(t *testing.T) {
	assertAssetKeyRejected(t, []rejectCase{
		{"backslash", `a\b.png`},
		{"nul", "a\x00.png"},
		{"newline", "a\nb.png"},
		{"invalid utf-8", "a\xff.png"},
	})
}

func TestNewAssetKeyRejectsOversizedKeys(t *testing.T) {
	assertAssetKeyRejected(t, []rejectCase{
		{"oversized key", strings.Repeat("dir/", 500) + "a.png"},
		{"oversized segment", strings.Repeat("a", 300) + ".png"},
	})
}

func TestAssetKeyAccessors(t *testing.T) {
	k, err := NewAssetKey("uploads/2026/08/photo.jpg")
	if err != nil {
		t.Fatalf("NewAssetKey error = %v; want nil", err)
	}
	if got := k.String(); got != "uploads/2026/08/photo.jpg" {
		t.Errorf("String() = %q; want %q", got, "uploads/2026/08/photo.jpg")
	}
	if got := k.Base(); got != "photo.jpg" {
		t.Errorf("Base() = %q; want %q", got, "photo.jpg")
	}
	if got := k.Ext(); got != ".jpg" {
		t.Errorf("Ext() = %q; want %q", got, ".jpg")
	}
	if k.IsZero() {
		t.Errorf("IsZero() = true; want false")
	}

	var zero AssetKey
	if !zero.IsZero() {
		t.Errorf("zero IsZero() = false; want true")
	}
	if got := zero.String(); got != "" {
		t.Errorf("zero String() = %q; want %q", got, "")
	}
}

func TestAssetKeyTextRoundTrip(t *testing.T) {
	k, err := NewAssetKey("uploads/logo.png")
	if err != nil {
		t.Fatalf("NewAssetKey error = %v; want nil", err)
	}

	b, err := k.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText error = %v; want nil", err)
	}
	if string(b) != "uploads/logo.png" {
		t.Errorf("MarshalText = %q; want %q", b, "uploads/logo.png")
	}

	var got AssetKey
	if err := got.UnmarshalText(b); err != nil {
		t.Fatalf("UnmarshalText error = %v; want nil", err)
	}
	if got != k {
		t.Errorf("round trip = %#v; want %#v", got, k)
	}

	var bad AssetKey
	err = bad.UnmarshalText([]byte("../x.png"))
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("UnmarshalText(%q) error = %v; want errors.Is(err, ErrInvalid)", "../x.png", err)
	}
	if !bad.IsZero() {
		t.Errorf("UnmarshalText left %#v; want the receiver zero", bad)
	}
}
