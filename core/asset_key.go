package core

import "path"

// maxAssetKeyBytes is the largest an asset key may be. 1024 bytes is the
// smallest key length S3-compatible implementations commonly agree on, so it
// is a portability floor rather than one vendor's rule; a store with a tighter
// limit rejects at its own boundary.
const maxAssetKeyBytes = 1024

// AssetKey is a validated key naming an object in an asset store.
//
// It is deliberately a different type from ContentPath. Assets live outside
// Git, so a key addresses an object in a store rather than a file in a
// repository: the ".git" rule that guards content does not apply, and the
// length limit is the object store's, not the filesystem's. Keeping the types
// apart means a content path cannot be passed where a key is meant.
//
// The zero value is not a usable key; NewAssetKey is the only way to obtain a
// valid one. An AssetKey is comparable and can be used as a map key.
type AssetKey struct {
	k string
}

// NewAssetKey validates s and returns it as an AssetKey.
//
// s must be a non-empty, relative, already-cleaned slash key: valid UTF-8,
// free of control characters and backslashes, without a leading or trailing
// slash and without an empty, "." or ".." segment. As with content paths,
// invalid input is rejected rather than normalized.
//
// The returned error unwraps to ErrInvalid.
func NewAssetKey(s string) (AssetKey, error) {
	if err := validateRelPath("asset key", s, maxAssetKeyBytes); err != nil {
		return AssetKey{}, err
	}
	return AssetKey{k: s}, nil
}

// String returns the key, or the empty string for the zero value.
func (k AssetKey) String() string { return k.k }

// IsZero reports whether k is the zero value, which is not a valid key.
func (k AssetKey) IsZero() bool { return k.k == "" }

// Base returns the final segment of the key.
func (k AssetKey) Base() string { return path.Base(k.k) }

// Ext returns the extension of the final segment, including the leading dot,
// or the empty string when there is none.
func (k AssetKey) Ext() string { return path.Ext(k.k) }

// MarshalText implements encoding.TextMarshaler.
func (k AssetKey) MarshalText() ([]byte, error) { return []byte(k.k), nil }

// UnmarshalText implements encoding.TextUnmarshaler. It validates the text the
// same way NewAssetKey does and leaves the receiver unchanged on failure.
func (k *AssetKey) UnmarshalText(b []byte) error {
	parsed, err := NewAssetKey(string(b))
	if err != nil {
		return err
	}
	*k = parsed
	return nil
}
