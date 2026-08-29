package core

import (
	"fmt"
	"mime"
	"strings"
	"time"
)

// Digest is an opaque content digest in "algorithm:hex" form, such as
// "sha256:e3b0c442...".
//
// It is a string rather than a typed hash so that a store which can only
// expose an opaque checksum is not forced to claim an algorithm it does not
// know. The zero Digest means the store supplied none.
type Digest string

// NewDigest builds a Digest from an algorithm name and a hex-encoded value.
// The algorithm must be non-empty and contain no ":"; the hex must be
// non-empty, of even length and made only of hex digits. It is canonicalized
// to lower case so that two digests of the same bytes compare equal.
//
// The returned error unwraps to ErrInvalid.
func NewDigest(algorithm, hex string) (Digest, error) {
	const kind = "digest"

	if algorithm == "" {
		return "", invalidf(kind, algorithm, "has an empty algorithm")
	}
	if strings.Contains(algorithm, ":") {
		return "", invalidf(kind, algorithm, `algorithm contains a ":"`)
	}
	lower := strings.ToLower(hex)
	if err := validateHex(kind, algorithm+":"+lower, lower); err != nil {
		return "", err
	}
	return Digest(algorithm + ":" + lower), nil
}

// Algorithm returns the part before the first ":", or the empty string when d
// is zero or malformed.
func (d Digest) Algorithm() string {
	algorithm, _, ok := strings.Cut(string(d), ":")
	if !ok {
		return ""
	}
	return algorithm
}

// Hex returns the part after the first ":", or the empty string when d is zero
// or malformed.
func (d Digest) Hex() string {
	_, hex, ok := strings.Cut(string(d), ":")
	if !ok {
		return ""
	}
	return hex
}

// IsZero reports whether d is the empty digest.
func (d Digest) IsZero() bool { return d == "" }

// Validate reports whether d is well formed. The zero Digest is valid: a
// digest is optional, because not every store can supply one.
//
// The returned error unwraps to ErrInvalid.
func (d Digest) Validate() error {
	const kind = "digest"

	if d.IsZero() {
		return nil
	}
	algorithm, hex, ok := strings.Cut(string(d), ":")
	if !ok {
		return invalidf(kind, string(d), `is not in "algorithm:hex" form`)
	}
	if algorithm == "" {
		return invalidf(kind, string(d), "has an empty algorithm")
	}
	return validateHex(kind, string(d), hex)
}

// SizeUnknown is the Asset.Size of a stream whose length the caller does not
// know in advance. A store may still record the real size once it has written
// the object.
const SizeUnknown int64 = -1

// Asset is a request to store a binary object: where it goes and what the
// caller knows about it.
//
// The bytes are not here. They travel beside the Asset as an io.Reader on
// AssetStore.Put, because an asset can be far larger than anything that
// belongs in a domain value, and because a struct that is sometimes consumed
// and sometimes not is a trap.
type Asset struct {
	// Key is where the object is stored.
	Key AssetKey
	// ContentType is the media type, such as "image/png". It may be empty when
	// the caller does not know it.
	ContentType string
	// Size is the length in bytes, or SizeUnknown.
	Size int64
}

// Validate reports whether a is well formed: it has a key, a size that is
// either non-negative or SizeUnknown, and a content type that either is empty
// or parses as a media type.
//
// The returned error unwraps to ErrInvalid.
func (a Asset) Validate() error {
	const kind = "asset"

	if a.Key.IsZero() {
		return invalidf(kind, "", "has no key")
	}
	if a.Size < SizeUnknown {
		return invalidf(kind, a.Key.String(), "has a size of %d, which is neither a length nor SizeUnknown", a.Size)
	}
	return validateMediaType(kind, a.Key.String(), a.ContentType)
}

// AssetRef is what a store reports about an object it holds.
//
// It carries no URL. A public or signed URL is a delivery concern — that is
// where a CDN would appear — and putting one here would be the seam through
// which one vendor's delivery model entered the domain. Digest and ModTime are
// optional, because not every store can supply them.
type AssetRef struct {
	// Key is where the object is stored.
	Key AssetKey
	// Size is the stored length in bytes.
	Size int64
	// ContentType is the media type the store records, if any.
	ContentType string
	// Digest is the store's content digest, if it supplies one.
	Digest Digest
	// ModTime is when the store last modified the object, if it knows.
	ModTime time.Time
}

// Validate reports whether r is well formed: it has a key, a non-negative size
// — a stored object has a known length — a content type that either is empty
// or parses, and a digest that either is absent or is well formed.
//
// The returned error unwraps to ErrInvalid.
func (r AssetRef) Validate() error {
	const kind = "asset ref"

	if r.Key.IsZero() {
		return invalidf(kind, "", "has no key")
	}
	if r.Size < 0 {
		return invalidf(kind, r.Key.String(), "has a negative size of %d", r.Size)
	}
	if err := validateMediaType(kind, r.Key.String(), r.ContentType); err != nil {
		return err
	}
	if err := r.Digest.Validate(); err != nil {
		return fmt.Errorf("asset ref %q: %w", r.Key, err)
	}
	return nil
}

// validateHex checks that hex is a non-empty, even-length string of lower-case
// hex digits. kind and value name the enclosing value for the error message.
func validateHex(kind, value, hex string) error {
	if hex == "" {
		return invalidf(kind, value, "has an empty hex value")
	}
	if len(hex)%2 != 0 {
		return invalidf(kind, value, "has an odd-length hex value of %d characters", len(hex))
	}
	for _, r := range hex {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return invalidf(kind, value, "has a non-hex character %q in its hex value", r)
		}
	}
	return nil
}

// validateMediaType checks that mediaType is empty or parses as a media type.
// kind and value name the enclosing value for the error message.
func validateMediaType(kind, value, mediaType string) error {
	if mediaType == "" {
		return nil
	}
	if _, _, err := mime.ParseMediaType(mediaType); err != nil {
		return invalidf(kind, value, "has an unparseable content type %q: %v", mediaType, err)
	}
	return nil
}
