package localfs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shutx-net/micawber/core"
)

// watchdog is how long a test waits for a call that must not block. It is
// generous on purpose: the operations it guards take microseconds, so the only
// thing it can catch is an operation that blocks forever.
const watchdog = 5 * time.Second

// TestStatReportsSizeModTimeAndDerivedContentType covers what Stat knows: the
// size and modification time from the directory entry, and a content type
// derived from the key's extension.
func TestStatReportsSizeModTimeAndDerivedContentType(t *testing.T) {
	d := newTestDir(t)
	body := []byte("not really a png, but the right length")
	d.drop("img/logo.png", body)
	s := d.open()

	key := mustKey(t, "img/logo.png")
	ref, err := s.Stat(t.Context(), key)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	fi, err := os.Lstat(d.join("img/logo.png"))
	if err != nil {
		t.Fatalf("lstat the object directly: %v", err)
	}

	if ref.Key != key {
		t.Errorf("Key = %q, want %q", ref.Key, key)
	}
	if ref.Size != int64(len(body)) {
		t.Errorf("Size = %d, want %d", ref.Size, len(body))
	}
	if ref.ContentType != "image/png" {
		t.Errorf("ContentType = %q, want %q", ref.ContentType, "image/png")
	}
	if !ref.ModTime.Equal(fi.ModTime()) {
		t.Errorf("ModTime = %v, want the entry's own %v", ref.ModTime, fi.ModTime())
	}
}

// TestStatReportsNoDigest asserts the asymmetry the store is honest about.
//
// core/store.go says Stat reports what the store knows "without reading it", so
// a digest is not available: recomputing one would make an O(1) call O(size),
// and there is nowhere to have stored one that a file dropped in by hand would
// also have. core.Digest's zero value means the store supplied none, which is
// the truth here.
func TestStatReportsNoDigest(t *testing.T) {
	d := newTestDir(t)
	d.drop("logo.png", []byte("bytes"))
	s := d.open()

	ref, err := s.Stat(t.Context(), mustKey(t, "logo.png"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !ref.Digest.IsZero() {
		t.Errorf("Digest = %q, want the zero digest", ref.Digest)
	}
}

// TestStatReturnsAValidAssetRef keeps the store's output inside the domain's
// own rules, which is what stops a typo in the media-type table surfacing as a
// validation failure on an object that is perfectly fine.
func TestStatReturnsAValidAssetRef(t *testing.T) {
	d := newTestDir(t)
	for _, name := range []string{"logo.png", "notes.md", "model.glb", "README", "empty.png"} {
		d.drop(name, []byte("x"))
	}
	s := d.open()

	for _, name := range []string{"logo.png", "notes.md", "model.glb", "README", "empty.png"} {
		t.Run(name, func(t *testing.T) {
			ref, err := s.Stat(t.Context(), mustKey(t, name))
			if err != nil {
				t.Fatalf("Stat: %v", err)
			}
			if err := ref.Validate(); err != nil {
				t.Errorf("the ref Stat returned does not validate: %v", err)
			}
		})
	}
}

// TestStatOfAMissingKeyIsNotFound covers the ordinary absence, and the absent
// intermediate directory beside it.
func TestStatOfAMissingKeyIsNotFound(t *testing.T) {
	d := newTestDir(t)
	s := d.open()

	for _, name := range []string{"missing.png", "no/such/dir/logo.png"} {
		t.Run(name, func(t *testing.T) {
			_, err := s.Stat(t.Context(), mustKey(t, name))
			if !errors.Is(err, core.ErrNotFound) {
				t.Errorf("Stat(%q) = %v, want an error matching core.ErrNotFound", name, err)
			}
		})
	}
}

// TestStatOfADirectoryIsNotFound asserts the regular-files-only rule for the
// case a caller is most likely to hit by accident.
func TestStatOfADirectoryIsNotFound(t *testing.T) {
	d := newTestDir(t)
	d.mkdir("img")
	s := d.open()

	if _, err := s.Stat(t.Context(), mustKey(t, "img")); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Stat of a directory = %v, want an error matching core.ErrNotFound", err)
	}
}

// TestStatOfASymlinkIsNotFoundWhereverItPoints is the one that matters.
//
// All three cases must be the same answer. A test that only covered the
// escaping link would let a later "follow links that stay inside" change pass,
// and that change is what would turn the store into an oracle for the
// filesystem outside the directory: three distinguishable answers is enough to
// map the operator's symlinks.
func TestStatOfASymlinkIsNotFoundWhereverItPoints(t *testing.T) {
	skipWithoutSymlinks(t)

	d := newTestDir(t)
	d.drop("real.png", []byte("inside the root"))
	d.outsideFile("SECRET-OUTSIDE", []byte("SECRET-OUTSIDE-THE-ROOT"))
	d.link("real.png", "inside.png")
	d.link("../outside/SECRET-OUTSIDE", "escaping.png")
	d.link("nowhere.png", "dangling.png")
	s := d.open()

	var errs []error
	for _, name := range []string{"inside.png", "escaping.png", "dangling.png"} {
		_, err := s.Stat(t.Context(), mustKey(t, name))
		if !errors.Is(err, core.ErrNotFound) {
			t.Errorf("Stat(%q) = %v, want an error matching core.ErrNotFound", name, err)
		}
		errs = append(errs, err)
	}

	// Indistinguishable in class from each other and from a plain missing key.
	_, missing := s.Stat(t.Context(), mustKey(t, "not-there.png"))
	for _, err := range append(errs, missing) {
		for _, sentinel := range []error{core.ErrNotFound, core.ErrInvalid, core.ErrExists, core.ErrConflict} {
			want := errors.Is(missing, sentinel)
			if got := errors.Is(err, sentinel); got != want {
				t.Errorf("%v classifies differently from a missing key against %v: %v, want %v", err, sentinel, got, want)
			}
		}
	}
}

// TestStatOfAFifoIsNotFound covers the entry that would otherwise hang a
// request thread forever.
func TestStatOfAFifoIsNotFound(t *testing.T) {
	skipWithoutFifo(t)

	d := newTestDir(t)
	d.fifo("trap.png")
	s := d.open()

	if _, err := s.Stat(t.Context(), mustKey(t, "trap.png")); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Stat of a FIFO = %v, want an error matching core.ErrNotFound", err)
	}
}

// TestStatDoesNotReadTheObject is written against a FIFO rather than against a
// clock.
//
// open(2) on a FIFO with no writer blocks indefinitely, so a Stat that opened
// the object could not return at all; a Stat that only asks the directory
// returns in microseconds. Asserting a duration on a large file instead would
// be flaky, since t.TempDir() may be on tmpfs. The other half of the statement
// — that Stat computes no digest — is asserted on an object large enough that
// hashing it would be conspicuous.
func TestStatDoesNotReadTheObject(t *testing.T) {
	skipWithoutFifo(t)

	d := newTestDir(t)
	d.fifo("trap.png")
	d.drop("big.png", bytes.Repeat([]byte("m"), 4<<20))
	s := d.open()

	done := make(chan error, 1)
	go func() {
		_, err := s.Stat(context.WithoutCancel(t.Context()), mustKey(t, "trap.png"))
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, core.ErrNotFound) {
			t.Errorf("Stat of a FIFO = %v, want an error matching core.ErrNotFound", err)
		}
	case <-time.After(watchdog):
		t.Fatalf("Stat of a FIFO blocked for %v: it opened the object, which the regular-files-only rule exists to prevent", watchdog)
	}

	ref, err := s.Stat(t.Context(), mustKey(t, "big.png"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !ref.Digest.IsZero() {
		t.Errorf("Digest = %q, want the zero digest: Stat may not read the object", ref.Digest)
	}
}

// TestStatOfAnUnknownExtensionHasAnEmptyContentType asserts the cost of the
// fixed table, so that anybody who dislikes it has to change a test that says
// what the trade is.
func TestStatOfAnUnknownExtensionHasAnEmptyContentType(t *testing.T) {
	d := newTestDir(t)
	d.drop("scene.glb", []byte("x"))
	d.drop("README", []byte("x"))
	s := d.open()

	for _, name := range []string{"scene.glb", "README"} {
		t.Run(name, func(t *testing.T) {
			ref, err := s.Stat(t.Context(), mustKey(t, name))
			if err != nil {
				t.Fatalf("Stat: %v", err)
			}
			if ref.ContentType != "" {
				t.Errorf("ContentType = %q, want the empty string", ref.ContentType)
			}
			if err := ref.Validate(); err != nil {
				t.Errorf("a ref with no content type does not validate: %v", err)
			}
		})
	}
}

// TestStatOfAZeroLengthObject asserts that zero is a length like any other.
func TestStatOfAZeroLengthObject(t *testing.T) {
	d := newTestDir(t)
	d.drop("empty.png", nil)
	s := d.open()

	ref, err := s.Stat(t.Context(), mustKey(t, "empty.png"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if ref.Size != 0 {
		t.Errorf("Size = %d, want 0", ref.Size)
	}
	if err := ref.Validate(); err != nil {
		t.Errorf("a zero-length ref does not validate: %v", err)
	}
}

// TestStatHonoursCancellation asserts that a done context is reported before
// anything is touched.
func TestStatHonoursCancellation(t *testing.T) {
	d := newTestDir(t)
	d.drop("logo.png", []byte("x"))
	s := d.open()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := s.Stat(ctx, mustKey(t, "logo.png")); !errors.Is(err, context.Canceled) {
		t.Errorf("Stat with a cancelled context = %v, want an error matching context.Canceled", err)
	}
}

// TestStatValidatesTheKey asserts that this store's added key rules are applied
// on the read path too, so that the set of addressable keys is one set.
func TestStatValidatesTheKey(t *testing.T) {
	d := newTestDir(t)
	d.drop("nul.png", []byte("unreachable"))
	s := d.open()

	if _, err := s.Stat(t.Context(), mustKey(t, "nul.png")); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("Stat of a reserved name = %v, want an error matching core.ErrInvalid", err)
	}
}

// TestFilesystemCaseBehaviourIsRecorded states the truth about the host the
// suite is running on rather than encoding one platform's answer.
//
// Logo.PNG and logo.png are two objects on Linux and one on a default macOS or
// Windows volume. The store does nothing about that on purpose: lower-casing
// keys would turn a quirk of two platforms into a rule for all four, and
// rejecting a key that differs only in case from an existing one would mean
// reading the whole directory on every Put and would be a race even then. This
// test never skips, so on any host the suite says which of the two worlds it
// ran in.
func TestFilesystemCaseBehaviourIsRecorded(t *testing.T) {
	d := newTestDir(t)
	d.drop("Logo.PNG", []byte("first"))
	d.drop("logo.png", []byte("second"))
	s := d.open()

	entries := d.entries()
	switch len(entries) {
	case 2:
		t.Logf("this filesystem is case-sensitive: %v are two objects", entries)
		upper, err := s.Stat(t.Context(), mustKey(t, "Logo.PNG"))
		if err != nil {
			t.Fatalf("Stat Logo.PNG: %v", err)
		}
		lower, err := s.Stat(t.Context(), mustKey(t, "logo.png"))
		if err != nil {
			t.Fatalf("Stat logo.png: %v", err)
		}
		if upper.Size == lower.Size && string(d.raw("Logo.PNG")) == string(d.raw("logo.png")) {
			t.Errorf("two entries exist but hold the same bytes, which is neither documented outcome")
		}
	case 1:
		t.Logf("this filesystem is case-insensitive: %v is one object", entries)
		if got := string(d.raw(entries[0])); got != "second" {
			t.Errorf("the single entry holds %q, want the second write %q", got, "second")
		}
	default:
		t.Fatalf("writing two keys differing only in case produced %d entries (%v), which is neither documented outcome", len(entries), entries)
	}
}

// TestStatOfATrailingSlashKeyIsRefused is a reminder that core's own rules run
// first: a key cannot name a directory by ending in a slash, because
// core.NewAssetKey refuses one.
func TestStatOfATrailingSlashKeyIsRefused(t *testing.T) {
	if _, err := core.NewAssetKey("img/"); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("core.NewAssetKey(%q) = %v, want an error matching core.ErrInvalid", "img/", err)
	}
	if _, err := core.NewAssetKey(strings.Repeat("x", 2000)); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("core.NewAssetKey of an over-long key = %v, want an error matching core.ErrInvalid", err)
	}
}

// TestPutThenGetThenStatRoundTripsEveryTableExtension walks the whole
// media-type table, so a value that is in the map but wrong is caught by the
// operations rather than only by the table's own test.
func TestPutThenGetThenStatRoundTripsEveryTableExtension(t *testing.T) {
	d := newTestDir(t)
	s := d.open()

	if len(mediaTypes) == 0 {
		t.Fatalf("the media-type table is empty; the round trip would prove nothing")
	}
	for ext, want := range mediaTypes {
		t.Run(ext, func(t *testing.T) {
			key := mustKey(t, "assets/object"+ext)
			body := []byte("a small object stored at " + key.String())

			written, err := s.Put(t.Context(), core.Asset{Key: key, Size: int64(len(body))}, bytes.NewReader(body))
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			if written.ContentType != want {
				t.Errorf("Put's ContentType = %q, want %q", written.ContentType, want)
			}

			stated, err := s.Stat(t.Context(), key)
			if err != nil {
				t.Fatalf("Stat: %v", err)
			}
			if stated.ContentType != want {
				t.Errorf("Stat's ContentType = %q, want %q", stated.ContentType, want)
			}
			if err := stated.Validate(); err != nil {
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
			if !bytes.Equal(got, body) {
				t.Errorf("read %q, want %q", got, body)
			}
		})
	}
}
