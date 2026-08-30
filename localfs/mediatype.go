package localfs

import (
	"mime"
	"strings"

	"github.com/shutx-net/micawber/core"
)

// mediaTypes maps a lower-case file extension to the media type this store
// reports for a key that ends in it.
//
// It is fixed, and deliberately not mime.TypeByExtension. Go's mime package
// augments its built-in table at init from /etc/mime.types, the Apache and
// httpd files and the XDG globs2 databases, so its answer is a property of the
// host rather than of the key: measured on a machine with /etc/mime.types
// present, ".otf" resolves to "application/vnd.oasis.opendocument.formula-template",
// an OpenType font served as an OpenDocument formula template, and on a host
// without that file it resolves to the empty string. Portability is a product
// requirement, and a store that reports a different content type for the same
// object on the deployment host than on the developer's laptop is not portable.
//
// There is no fallback to the standard library for a miss. A fallback would
// reintroduce host dependence for exactly the long tail where nobody would
// notice it, and an unknown extension has an honest answer available: the empty
// string, which [core.AssetRef.Validate] explicitly permits. [WithMediaTypes]
// is how a deployment adds one.
//
// Every value here is one this project chose, which matters beyond tidiness:
// the derived content type is what an HTTP layer will put in a Content-Type
// header, so an attacker-influenced extension resolving through a host file the
// project does not control would be a header-injection surface by proxy.
var mediaTypes = map[string]string{
	// Raster and vector images, which are most of what a CMS stores.
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".avif": "image/avif",
	".heic": "image/heic",
	".heif": "image/heif",
	".bmp":  "image/bmp",
	".tif":  "image/tiff",
	".tiff": "image/tiff",
	".ico":  "image/x-icon",
	".svg":  "image/svg+xml",

	// Fonts.
	".otf":   "font/otf",
	".ttf":   "font/ttf",
	".woff":  "font/woff",
	".woff2": "font/woff2",

	// Documents and text. The charset is stated rather than left to the client
	// to guess, because everything Micawber writes is UTF-8.
	".pdf":  "application/pdf",
	".txt":  "text/plain; charset=utf-8",
	".md":   "text/markdown; charset=utf-8",
	".csv":  "text/csv; charset=utf-8",
	".html": "text/html; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".json": "application/json",
	".xml":  "application/xml",
	".yaml": "application/yaml",
	".yml":  "application/yaml",
	".toml": "application/toml",

	// Audio.
	".mp3":  "audio/mpeg",
	".m4a":  "audio/mp4",
	".ogg":  "audio/ogg",
	".opus": "audio/ogg",
	".wav":  "audio/wav",
	".flac": "audio/flac",

	// Video.
	".mp4":  "video/mp4",
	".m4v":  "video/mp4",
	".webm": "video/webm",
	".mov":  "video/quicktime",

	// Archives.
	".zip": "application/zip",
	".gz":  "application/gzip",
	".tar": "application/x-tar",
	".7z":  "application/x-7z-compressed",
}

// mediaTypeFor returns the media type for key's extension, preferring extra
// when it has an entry, or the empty string when neither table knows it.
//
// The extension is folded to lower case for the lookup and the key itself is
// untouched: case is the filesystem's business, not the store's.
func mediaTypeFor(key core.AssetKey, extra map[string]string) string {
	ext := strings.ToLower(key.Ext())
	if ext == "" {
		return ""
	}
	if value, ok := extra[ext]; ok {
		return value
	}
	return mediaTypes[ext]
}

// checkMediaTypes validates a table supplied to [WithMediaTypes].
//
// A key must be a lower-case dotted extension of the shape [core.AssetKey.Ext]
// produces, because an entry that cannot ever be looked up is a
// misconfiguration that would otherwise fail silently: ".tar.gz" never matches,
// since the extension of "backup.tar.gz" is ".gz". A value must parse with
// mime.ParseMediaType, which is the same check [core.AssetRef.Validate] makes,
// so a typo is reported at Open rather than as a validation failure on an
// object that is perfectly fine.
//
// The returned error unwraps to [core.ErrInvalid].
func checkMediaTypes(m map[string]string) error {
	for ext, value := range m {
		switch {
		case !strings.HasPrefix(ext, "."):
			return sentinelf(core.ErrInvalid, nil, "WithMediaTypes: %q is not a dotted extension", ext)
		case len(ext) < 2:
			return sentinelf(core.ErrInvalid, nil, "WithMediaTypes: %q has no extension after the dot", ext)
		case ext != strings.ToLower(ext):
			return sentinelf(core.ErrInvalid, nil, "WithMediaTypes: %q is not lower case, so it could never be looked up", ext)
		case strings.ContainsAny(ext[1:], "./\\"):
			return sentinelf(core.ErrInvalid, nil, "WithMediaTypes: %q is not a single extension, so it could never be looked up", ext)
		}
		if _, _, err := mime.ParseMediaType(value); err != nil {
			return sentinelf(core.ErrInvalid, err, "WithMediaTypes: the value %q for %q is not a media type", value, ext)
		}
	}
	return nil
}
