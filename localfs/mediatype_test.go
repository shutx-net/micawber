package localfs

import (
	"errors"
	"mime"
	"strings"
	"testing"

	"github.com/shutx-net/micawber/core"
)

// TestMediaTypeTableAnswers pins the answers for the extensions a CMS actually
// stores.
//
// ".otf" is the entry that decided the design and is why it is asserted here
// rather than left to the table: on this machine mime.TypeByExtension resolves
// it to "application/vnd.oasis.opendocument.formula-template", an OpenType font
// served as an OpenDocument formula template, because Go's mime package reads
// /etc/mime.types at init. The table this package ships is fixed, so every type
// the store can ever report was chosen here.
func TestMediaTypeTableAnswers(t *testing.T) {
	cases := map[string]string{
		"logo.png":        "image/png",
		"photos/hero.jpg": "image/jpeg",
		"hero.jpeg":       "image/jpeg",
		"anim.gif":        "image/gif",
		"hero.webp":       "image/webp",
		"hero.avif":       "image/avif",
		"hero.heic":       "image/heic",
		"mark.svg":        "image/svg+xml",
		"body.otf":        "font/otf",
		"body.ttf":        "font/ttf",
		"body.woff2":      "font/woff2",
		"report.pdf":      "application/pdf",
		"notes.md":        "text/markdown; charset=utf-8",
		"data.json":       "application/json",
		"clip.mp4":        "video/mp4",
		"clip.webm":       "video/webm",
		"track.mp3":       "audio/mpeg",
		"bundle.zip":      "application/zip",
	}

	for key, want := range cases {
		t.Run(key, func(t *testing.T) {
			if got := mediaTypeFor(mustKey(t, key), nil); got != want {
				t.Errorf("mediaTypeFor(%q) = %q, want %q", key, got, want)
			}
		})
	}
}

// TestMediaTypeIsEmptyForAnUnknownExtension asserts the honest answer.
//
// There is deliberately no fallback to mime.TypeByExtension for a miss: a
// fallback would reintroduce host dependence for exactly the long tail where
// nobody would notice it. The empty string is what core.AssetRef.Validate
// explicitly permits, and WithMediaTypes is how an operator adds ".glb".
func TestMediaTypeIsEmptyForAnUnknownExtension(t *testing.T) {
	for _, key := range []string{"model.glb", "archive.xyz", "README", "img/no-extension", "trailing."} {
		t.Run(key, func(t *testing.T) {
			if got := mediaTypeFor(mustKey(t, key), nil); got != "" {
				t.Errorf("mediaTypeFor(%q) = %q, want the empty string", key, got)
			}
		})
	}
}

// TestMediaTypeLookupIsCaseInsensitiveOnTheExtension asserts that ".PNG" and
// ".png" agree. The key itself is passed through byte for byte; only the
// extension lookup folds case.
func TestMediaTypeLookupIsCaseInsensitiveOnTheExtension(t *testing.T) {
	for _, key := range []string{"logo.PNG", "logo.Png", "logo.pNg"} {
		t.Run(key, func(t *testing.T) {
			if got := mediaTypeFor(mustKey(t, key), nil); got != "image/png" {
				t.Errorf("mediaTypeFor(%q) = %q, want %q", key, got, "image/png")
			}
		})
	}
}

// TestEveryTableValueParsesAsAMediaType is what makes the AssetRef the store
// returns pass core.AssetRef.Validate, which parses the content type the same
// way. A typo in the table would otherwise surface as a validation failure on
// an object that is perfectly fine.
func TestEveryTableValueParsesAsAMediaType(t *testing.T) {
	if len(mediaTypes) == 0 {
		t.Fatalf("the media-type table is empty; the guard would pass vacuously")
	}

	for ext, value := range mediaTypes {
		if !strings.HasPrefix(ext, ".") || ext != strings.ToLower(ext) {
			t.Errorf("table key %q is not a lower-case dotted extension", ext)
		}
		if _, _, err := mime.ParseMediaType(value); err != nil {
			t.Errorf("table value %q for %q does not parse: %v", value, ext, err)
		}
	}
}

// TestWithMediaTypesExtendsAndOverrides covers the one option this store has.
// D8 replaced the standard library's extension table with a fixed one, so a
// deployment that stores ".glb" models needs a way to say so short of patching
// the source.
func TestWithMediaTypesExtendsAndOverrides(t *testing.T) {
	d := newTestDir(t)
	s := d.open(WithMediaTypes(map[string]string{
		".glb": "model/gltf-binary",
		".png": "image/x-custom-png",
	}))

	if got := mediaTypeFor(mustKey(t, "scene.glb"), s.media); got != "model/gltf-binary" {
		t.Errorf("extension added by WithMediaTypes = %q, want %q", got, "model/gltf-binary")
	}
	if got := mediaTypeFor(mustKey(t, "logo.png"), s.media); got != "image/x-custom-png" {
		t.Errorf("extension overridden by WithMediaTypes = %q, want %q", got, "image/x-custom-png")
	}
	if got := mediaTypeFor(mustKey(t, "hero.jpg"), s.media); got != "image/jpeg" {
		t.Errorf("untouched extension = %q, want %q", got, "image/jpeg")
	}
	if got := mediaTypeFor(mustKey(t, "logo.png"), nil); got != "image/png" {
		t.Errorf("the shipped table was mutated: mediaTypeFor(%q) = %q, want %q", "logo.png", got, "image/png")
	}
}

// TestWithMediaTypesRejectsAMalformedEntry asserts that a misconfiguration is
// reported at Open, before anything is written, rather than becoming a content
// type no HTTP client can parse.
func TestWithMediaTypesRejectsAMalformedEntry(t *testing.T) {
	cases := map[string]map[string]string{
		"missing dot":           {"glb": "model/gltf-binary"},
		"upper case extension":  {".GLB": "model/gltf-binary"},
		"dot only":              {".": "model/gltf-binary"},
		"empty extension":       {"": "model/gltf-binary"},
		"embedded separator":    {".a/b": "model/gltf-binary"},
		"compound extension":    {".tar.gz": "application/gzip"},
		"empty value":           {".glb": ""},
		"unparseable value":     {".glb": "model/gltf binary"},
		"missing subtype token": {".glb": "model//binary"},
		"malformed parameter":   {".glb": "model/gltf-binary; charset"},
	}

	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			d := newTestDir(t)
			_, err := Open(t.Context(), d.path, WithMediaTypes(m))
			if !errors.Is(err, core.ErrInvalid) {
				t.Fatalf("Open with %v = %v, want an error matching core.ErrInvalid", m, err)
			}
			if got := d.entries(); len(got) != 0 {
				t.Errorf("a rejected option still touched the directory: %v", got)
			}
		})
	}
}
