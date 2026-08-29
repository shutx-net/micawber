package core

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// mustAssetKey builds an AssetKey or fails the test.
func mustAssetKey(t *testing.T, s string) AssetKey {
	t.Helper()
	k, err := NewAssetKey(s)
	if err != nil {
		t.Fatalf("NewAssetKey(%q) error = %v; want nil", s, err)
	}
	return k
}

func TestNewDigest(t *testing.T) {
	lower := strings.Repeat("ab", 32)
	upper := strings.Repeat("AB", 32)

	t.Run("lowercase hex", func(t *testing.T) {
		got, err := NewDigest("sha256", lower)
		if err != nil {
			t.Fatalf("NewDigest error = %v; want nil", err)
		}
		if want := Digest("sha256:" + lower); got != want {
			t.Errorf("NewDigest = %q; want %q", got, want)
		}
	})

	t.Run("uppercase hex is lowercased", func(t *testing.T) {
		got, err := NewDigest("sha256", upper)
		if err != nil {
			t.Fatalf("NewDigest error = %v; want nil", err)
		}
		if want := Digest("sha256:" + lower); got != want {
			t.Errorf("NewDigest = %q; want %q", got, want)
		}
	})

	invalid := []struct {
		name      string
		algorithm string
		hex       string
	}{
		{"empty algorithm", "", lower},
		{"empty hex", "sha256", ""},
		{"non-hex", "sha256", strings.Repeat("zz", 32)},
		{"algorithm with colon", "sha:256", lower},
		{"odd length hex", "sha256", "abc"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewDigest(tc.algorithm, tc.hex)
			if err == nil {
				t.Fatalf("NewDigest(%q, %q) = %q, nil; want an error", tc.algorithm, tc.hex, got)
			}
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("NewDigest(%q, %q) error = %v; want errors.Is(err, ErrInvalid)", tc.algorithm, tc.hex, err)
			}
			if !got.IsZero() {
				t.Errorf("NewDigest(%q, %q) = %q; want the zero Digest", tc.algorithm, tc.hex, got)
			}
		})
	}
}

func TestDigestAccessors(t *testing.T) {
	hex := strings.Repeat("ab", 32)
	d, err := NewDigest("sha256", hex)
	if err != nil {
		t.Fatalf("NewDigest error = %v; want nil", err)
	}

	if got := d.Algorithm(); got != "sha256" {
		t.Errorf("Algorithm() = %q; want %q", got, "sha256")
	}
	if got := d.Hex(); got != hex {
		t.Errorf("Hex() = %q; want %q", got, hex)
	}
	if d.IsZero() {
		t.Errorf("IsZero() = true; want false")
	}
	if err := d.Validate(); err != nil {
		t.Errorf("Validate() error = %v; want nil", err)
	}

	t.Run("zero", func(t *testing.T) {
		var zero Digest
		if !zero.IsZero() {
			t.Errorf("IsZero() = false; want true")
		}
		if got := zero.Algorithm(); got != "" {
			t.Errorf("Algorithm() = %q; want %q", got, "")
		}
		if got := zero.Hex(); got != "" {
			t.Errorf("Hex() = %q; want %q", got, "")
		}
		if err := zero.Validate(); err != nil {
			t.Errorf("Validate() error = %v; want nil: a digest is optional", err)
		}
	})

	t.Run("malformed", func(t *testing.T) {
		malformed := Digest("not-a-digest")
		if got := malformed.Algorithm(); got != "" {
			t.Errorf("Algorithm() = %q; want %q", got, "")
		}
		if got := malformed.Hex(); got != "" {
			t.Errorf("Hex() = %q; want %q", got, "")
		}
		if err := malformed.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("Validate() error = %v; want errors.Is(err, ErrInvalid)", err)
		}
	})
}

func TestAssetValidate(t *testing.T) {
	key := mustAssetKey(t, "uploads/logo.png")

	tests := []struct {
		name  string
		asset Asset
		ok    bool
	}{
		{"full", Asset{Key: key, ContentType: "image/png", Size: 1024}, true},
		{"unknown size and no content type", Asset{Key: key, Size: SizeUnknown}, true},
		{"content type with parameters", Asset{Key: key, ContentType: "text/plain; charset=utf-8", Size: 0}, true},
		{"zero key", Asset{ContentType: "image/png", Size: 1024}, false},
		{"size below unknown", Asset{Key: key, Size: -2}, false},
		{"unparseable content type", Asset{Key: key, ContentType: "image//png", Size: 1}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.asset.Validate()
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

func TestAssetRefValidate(t *testing.T) {
	key := mustAssetKey(t, "uploads/logo.png")
	digest, err := NewDigest("sha256", strings.Repeat("ab", 32))
	if err != nil {
		t.Fatalf("NewDigest error = %v; want nil", err)
	}

	tests := []struct {
		name string
		ref  AssetRef
		ok   bool
	}{
		{"no digest", AssetRef{Key: key, Size: 1024, ContentType: "image/png"}, true},
		{"full", AssetRef{Key: key, Size: 1024, ContentType: "image/png", Digest: digest, ModTime: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)}, true},
		{"zero key", AssetRef{Size: 1024}, false},
		{"negative size", AssetRef{Key: key, Size: -1}, false},
		{"unparseable content type", AssetRef{Key: key, Size: 1, ContentType: "image//png"}, false},
		{"malformed digest", AssetRef{Key: key, Size: 1, Digest: Digest("sha256:xyz")}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.ref.Validate()
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
