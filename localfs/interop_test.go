package localfs

import (
	"bytes"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/shutx-net/micawber/core"
)

// TestStatOfAHandDroppedFileIsIndistinguishable is the payoff of the store
// holding no metadata of its own.
//
// The file is written with the os package, never through the Store: no Put has
// ever run, there is no index to build and no first-run scan, and yet every
// field of the AssetRef is populated exactly as it would be for an object the
// store wrote. A sidecar, an extended attribute or a manifest design cannot
// pass this test without a repair path.
func TestStatOfAHandDroppedFileIsIndistinguishable(t *testing.T) {
	d := newTestDir(t)
	body := []byte("the bytes of a photograph, near enough")
	d.drop("photos/hero.jpg", body)
	s := d.open()

	key := mustKey(t, "photos/hero.jpg")
	ref, err := s.Stat(t.Context(), key)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	fi, err := os.Lstat(d.join("photos/hero.jpg"))
	if err != nil {
		t.Fatalf("lstat the object directly: %v", err)
	}

	want := core.AssetRef{
		Key:         key,
		Size:        int64(len(body)),
		ContentType: "image/jpeg",
		ModTime:     fi.ModTime(),
	}
	if ref.Key != want.Key || ref.Size != want.Size || ref.ContentType != want.ContentType || !ref.ModTime.Equal(want.ModTime) {
		t.Errorf("Stat of a hand-dropped file = %+v, want %+v", ref, want)
	}
	if !ref.Digest.IsZero() {
		t.Errorf("Digest = %q, want the zero digest", ref.Digest)
	}
	if err := ref.Validate(); err != nil {
		t.Errorf("the ref does not validate: %v", err)
	}
}

// TestPutAndAHandDroppedFileAgreeInEveryField is the second half of the
// statement above: an object written by Put and a byte-identical file dropped
// in by hand Stat the same in every field a store can report.
//
// The modification times differ, because they are two files written at two
// moments, so the comparison is over everything else. That is not a hole: what
// the derive-only decision claims is that the store cannot tell the two apart,
// and a modification time is the filesystem's own record of each file rather
// than anything the store supplied.
func TestPutAndAHandDroppedFileAgreeInEveryField(t *testing.T) {
	d := newTestDir(t)
	body := []byte("the bytes of a photograph, near enough")
	d.drop("photos/dropped.jpg", body)
	s := d.open()

	put(t, s, "photos/written.jpg", body)

	dropped, err := s.Stat(t.Context(), mustKey(t, "photos/dropped.jpg"))
	if err != nil {
		t.Fatalf("Stat the hand-dropped file: %v", err)
	}
	written, err := s.Stat(t.Context(), mustKey(t, "photos/written.jpg"))
	if err != nil {
		t.Fatalf("Stat the object Put wrote: %v", err)
	}

	if dropped.Size != written.Size {
		t.Errorf("Size: hand-dropped %d, written %d", dropped.Size, written.Size)
	}
	if dropped.ContentType != written.ContentType {
		t.Errorf("ContentType: hand-dropped %q, written %q", dropped.ContentType, written.ContentType)
	}
	if dropped.Digest != written.Digest {
		t.Errorf("Digest: hand-dropped %q, written %q; Stat reports none for either", dropped.Digest, written.Digest)
	}
	if dropped.ModTime.IsZero() || written.ModTime.IsZero() {
		t.Errorf("a modification time is missing: hand-dropped %v, written %v", dropped.ModTime, written.ModTime)
	}
}

// TestAnExistingDirectoryTreeIsUsableWithoutMigration is the test that says
// there is no migration, no index build and no first-run scan.
//
// The tree is built with the os package — nested folders, mixed extensions, a
// zero-length file, a file with no extension, an extension the table does not
// know — and the store is opened on it and used immediately.
func TestAnExistingDirectoryTreeIsUsableWithoutMigration(t *testing.T) {
	d := newTestDir(t)

	tree := map[string]string{
		"logo.png":                     "a png",
		"img/2026/08/hero.jpg":         "a jpeg",
		"img/2026/08/hero.avif":        "an avif",
		"docs/handbook.pdf":            "a pdf",
		"docs/notes.md":                "# markdown\n",
		"models/scene.glb":             "an extension the table does not know",
		"README":                       "a file with no extension",
		"empty.png":                    "",
		"deep/a/b/c/d/e/f/g/thing.txt": "deeply nested",
	}
	for name, body := range tree {
		d.drop(name, []byte(body))
	}

	s := d.open()
	for name, body := range tree {
		t.Run(name, func(t *testing.T) {
			key := mustKey(t, name)

			ref, err := s.Stat(t.Context(), key)
			if err != nil {
				t.Fatalf("Stat: %v", err)
			}
			if ref.Size != int64(len(body)) {
				t.Errorf("Size = %d, want %d", ref.Size, len(body))
			}
			if err := ref.Validate(); err != nil {
				t.Errorf("the ref does not validate: %v", err)
			}

			r, err := s.Get(t.Context(), key)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			got, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if err := r.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
			if string(got) != body {
				t.Errorf("read %q, want %q", got, body)
			}
		})
	}
}

// TestObjectsWrittenByPutAreOrdinaryFiles is the converse: the store adds
// nothing to the directory that a human or another tool has to understand.
func TestObjectsWrittenByPutAreOrdinaryFiles(t *testing.T) {
	d := newTestDir(t)
	s := d.open()
	body := bytes.Repeat([]byte("ordinary"), 1000)

	put(t, s, "img/logo.png", body)

	raw, err := os.ReadFile(d.join("img/logo.png"))
	if err != nil {
		t.Fatalf("read the file with the os package: %v", err)
	}
	if !bytes.Equal(raw, body) {
		t.Errorf("the file holds %d bytes, want the %d written", len(raw), len(body))
	}

	fi, err := os.Lstat(d.join("img/logo.png"))
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if !fi.Mode().IsRegular() {
		t.Errorf("the object is %v, want a regular file", fi.Mode())
	}
	t.Logf("recorded: an object written under this process's umask has mode %v", fi.Mode().Perm())
}

// TestOtherToolsSeeWhatPutWrote states the same property from the other side:
// something that is not Micawber can read, replace and remove an object with no
// cooperation from the store, and the store agrees with it afterwards.
func TestOtherToolsSeeWhatPutWrote(t *testing.T) {
	d := newTestDir(t)
	s := d.open()
	key := mustKey(t, "img/logo.png")

	put(t, s, "img/logo.png", []byte("written by the store"))

	// Something else replaces the bytes.
	if err := os.WriteFile(d.join("img/logo.png"), []byte("replaced by something else"), 0o644); err != nil {
		t.Fatalf("replace the object: %v", err)
	}
	ref, err := s.Stat(t.Context(), key)
	if err != nil {
		t.Fatalf("Stat after the replacement: %v", err)
	}
	if ref.Size != int64(len("replaced by something else")) {
		t.Errorf("Size = %d, want the replaced length: there is no cached metadata to go stale", ref.Size)
	}

	// Something else removes it.
	if err := os.Remove(d.join("img/logo.png")); err != nil {
		t.Fatalf("remove the object: %v", err)
	}
	if _, err := s.Stat(t.Context(), key); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Stat after the removal = %v, want an error matching core.ErrNotFound", err)
	}
}

// TestStoreIgnoresFilesItCannotAddress asserts that a file no key can reach
// breaks nothing else. The store has no listing, so such a file is simply
// inert — but it is in the directory, and an operation on a neighbouring key
// must not notice.
func TestStoreIgnoresFilesItCannotAddress(t *testing.T) {
	skipWithoutSymlinks(t)

	d := newTestDir(t)
	d.drop("nul.png", []byte("a Windows device name"))
	d.drop("logo.png ", []byte("a trailing space"))
	d.drop("trailing.", []byte("a trailing dot"))
	d.link("logo.png", "alias.png")
	s := d.open()

	for _, name := range []string{"nul.png", "logo.png ", "trailing."} {
		if _, err := s.Stat(t.Context(), mustKey(t, name)); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("Stat(%q) = %v, want an error matching core.ErrInvalid", name, err)
		}
	}
	if _, err := s.Stat(t.Context(), mustKey(t, "alias.png")); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Stat of a symlink = %v, want an error matching core.ErrNotFound", err)
	}

	// The neighbours are unaffected.
	put(t, s, "logo.png", []byte("an ordinary object"))
	if got := string(d.raw("logo.png")); got != "an ordinary object" {
		t.Errorf("the ordinary object holds %q", got)
	}
	if got := string(d.raw("nul.png")); got != "a Windows device name" {
		t.Errorf("an unaddressable file was disturbed: %q", got)
	}
}

// TestNothingInTheRootIsCreatedExceptObjectsAndTheirDirectories is the test
// that catches something writing state into the operator's directory: no
// marker, no lock, no index, and no leftover temporary file.
func TestNothingInTheRootIsCreatedExceptObjectsAndTheirDirectories(t *testing.T) {
	d := newTestDir(t)
	s := d.open()

	put(t, s, "logo.png", []byte("one"))
	put(t, s, "img/2026/08/hero.jpg", []byte("two"))
	put(t, s, "img/2026/08/hero.jpg", []byte("two, replaced"))
	if _, err := s.Stat(t.Context(), mustKey(t, "logo.png")); err != nil {
		t.Fatalf("Stat: %v", err)
	}
	r, err := s.Get(t.Context(), mustKey(t, "logo.png"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := io.ReadAll(r); err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := s.Stat(t.Context(), mustKey(t, "never-written.png")); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Stat of a missing key: %v", err)
	}
	if err := s.Delete(t.Context(), mustKey(t, "logo.png")); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	want := []string{"img/", "img/2026/", "img/2026/08/", "img/2026/08/hero.jpg"}
	if got := d.entries(); !slices.Equal(got, want) {
		t.Errorf("after a full exercise the directory holds %v, want %v", got, want)
	}
	for _, name := range d.entries() {
		if strings.Contains(name, tempPrefix) {
			t.Errorf("a temporary file was left behind: %s", name)
		}
	}
}
