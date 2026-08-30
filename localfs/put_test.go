package localfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shutx-net/micawber/core"
)

// countingReader records how far a reader was advanced, which is what turns the
// LimitReader bound from a claim into an assertion.
type countingReader struct {
	r    io.Reader
	read int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += int64(n)
	return n, err
}

// failingReader yields good bytes and then fails, which is the ordinary shape
// of an upload that dies halfway.
type failingReader struct {
	body []byte
	at   int
	pos  int
	err  error
}

func (f *failingReader) Read(p []byte) (int, error) {
	if f.pos >= f.at {
		return 0, f.err
	}
	n := copy(p, f.body[f.pos:min(f.at, len(f.body))])
	f.pos += n
	return n, nil
}

// cancellingReader cancels the context partway through the stream, which is
// what a client disconnecting looks like from inside Put.
type cancellingReader struct {
	body   []byte
	at     int
	pos    int
	cancel context.CancelFunc
}

func (c *cancellingReader) Read(p []byte) (int, error) {
	if c.pos >= c.at {
		c.cancel()
	}
	if c.pos >= len(c.body) {
		return 0, io.EOF
	}
	n := copy(p, c.body[c.pos:])
	c.pos += n
	return n, nil
}

// put is the ordinary case, spelled once so that the tests below say what they
// are about.
func put(t *testing.T, s *Store, key string, body []byte) core.AssetRef {
	t.Helper()

	k := mustKey(t, key)
	ref, err := s.Put(t.Context(), core.Asset{Key: k, Size: int64(len(body))}, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Put %q: %v", key, err)
	}
	return ref
}

// sha256Of is the digest the store must report, computed by the test rather
// than by the code under test.
func sha256Of(t *testing.T, body []byte) core.Digest {
	t.Helper()

	sum := sha256.Sum256(body)
	d, err := core.NewDigest("sha256", hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatalf("build the expected digest: %v", err)
	}
	return d
}

// TestPutStoresTheBytesExactly is the byte-fidelity assertion on the write
// side, checked with the os package so that the store is not asked to confirm
// its own work.
func TestPutStoresTheBytesExactly(t *testing.T) {
	d := newTestDir(t)
	s := d.open()
	body := bytes.Repeat([]byte("\x00\xff binary \n\r"), 5000)

	put(t, s, "img/photo.jpg", body)

	if got := d.raw("img/photo.jpg"); !bytes.Equal(got, body) {
		t.Errorf("the file holds %d bytes, want the %d written", len(got), len(body))
	}
}

// TestPutReturnsARefThatValidates keeps the store's output inside the domain's
// own rules.
func TestPutReturnsARefThatValidates(t *testing.T) {
	d := newTestDir(t)
	s := d.open()

	ref := put(t, s, "img/logo.png", []byte("bytes"))
	if err := ref.Validate(); err != nil {
		t.Errorf("the ref Put returned does not validate: %v", err)
	}
	if ref.Key.String() != "img/logo.png" {
		t.Errorf("Key = %q, want %q", ref.Key, "img/logo.png")
	}
	if ref.ContentType != "image/png" {
		t.Errorf("ContentType = %q, want %q", ref.ContentType, "image/png")
	}
	if ref.ModTime.IsZero() {
		t.Errorf("ModTime is the zero time; Put knows when it wrote")
	}
}

// TestPutReturnsTheSha256OfTheBytes covers the one thing Put knows that Stat
// cannot. Hashing is nearly free where the bytes are already passing through,
// and a caller has a real use for it: recording the digest of an uploaded asset
// in a document's front matter is the integrity check a Git-native CMS wants.
func TestPutReturnsTheSha256OfTheBytes(t *testing.T) {
	d := newTestDir(t)
	s := d.open()
	body := []byte("the exact bytes")

	ref := put(t, s, "logo.png", body)
	if want := sha256Of(t, body); ref.Digest != want {
		t.Errorf("Digest = %q, want %q", ref.Digest, want)
	}
	if ref.Digest.Algorithm() != "sha256" {
		t.Errorf("Digest algorithm = %q, want %q", ref.Digest.Algorithm(), "sha256")
	}
}

// TestPutReturnsWhatStatWillReturnExceptTheDigest is the invariant written
// down, and it is what stops the asymmetry being "fixed" by accident.
//
// The two refs must agree in every field a store can report about an object,
// and disagree in exactly one: the digest, which Put has because the bytes went
// through it and Stat does not because it may not read them.
func TestPutReturnsWhatStatWillReturnExceptTheDigest(t *testing.T) {
	d := newTestDir(t)
	s := d.open()
	body := bytes.Repeat([]byte("a"), 4096)

	written := put(t, s, "img/2026/08/hero.jpg", body)
	stated, err := s.Stat(t.Context(), written.Key)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	if written.Key != stated.Key {
		t.Errorf("Key: Put %q, Stat %q", written.Key, stated.Key)
	}
	if written.Size != stated.Size {
		t.Errorf("Size: Put %d, Stat %d", written.Size, stated.Size)
	}
	if written.ContentType != stated.ContentType {
		t.Errorf("ContentType: Put %q, Stat %q", written.ContentType, stated.ContentType)
	}
	if !written.ModTime.Equal(stated.ModTime) {
		t.Errorf("ModTime: Put %v, Stat %v", written.ModTime, stated.ModTime)
	}
	if written.Digest != sha256Of(t, body) {
		t.Errorf("Put's Digest = %q, want the sha256 of the bytes", written.Digest)
	}
	if !stated.Digest.IsZero() {
		t.Errorf("Stat's Digest = %q, want the zero digest", stated.Digest)
	}
	for _, ref := range []core.AssetRef{written, stated} {
		if err := ref.Validate(); err != nil {
			t.Errorf("%+v does not validate: %v", ref, err)
		}
	}
}

// TestPutWithSizeUnknownRecordsWhatItRead covers the caller who does not know
// the length in advance, which is every streaming upload.
func TestPutWithSizeUnknownRecordsWhatItRead(t *testing.T) {
	d := newTestDir(t)
	s := d.open()
	body := bytes.Repeat([]byte("z"), 70_000)

	ref, err := s.Put(t.Context(), core.Asset{Key: mustKey(t, "big.png"), Size: core.SizeUnknown}, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if ref.Size != int64(len(body)) {
		t.Errorf("Size = %d, want %d", ref.Size, len(body))
	}
	if got := d.raw("big.png"); !bytes.Equal(got, body) {
		t.Errorf("the file holds %d bytes, want %d", len(got), len(body))
	}
}

// TestPutRejectsAStreamShorterThanTheDeclaredSize covers the direction with the
// worst available outcome if it were allowed: a short object published under a
// key the caller believes holds a complete one, which nothing later can detect.
func TestPutRejectsAStreamShorterThanTheDeclaredSize(t *testing.T) {
	d := newTestDir(t)
	s := d.open()
	body := []byte("only ten!!")

	_, err := s.Put(t.Context(), core.Asset{Key: mustKey(t, "logo.png"), Size: int64(len(body)) + 100}, bytes.NewReader(body))
	if !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Put with an over-declared size = %v, want an error matching core.ErrInvalid", err)
	}
	if _, err := s.Stat(t.Context(), mustKey(t, "logo.png")); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("a rejected Put published something: Stat = %v", err)
	}
	if got := d.entries(); len(got) != 0 {
		t.Errorf("a rejected Put left %v behind", got)
	}
}

// TestPutRejectsAStreamLongerThanTheDeclaredSize covers the other direction.
func TestPutRejectsAStreamLongerThanTheDeclaredSize(t *testing.T) {
	d := newTestDir(t)
	s := d.open()
	body := bytes.Repeat([]byte("q"), 5000)

	_, err := s.Put(t.Context(), core.Asset{Key: mustKey(t, "logo.png"), Size: 10}, bytes.NewReader(body))
	if !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Put with an under-declared size = %v, want an error matching core.ErrInvalid", err)
	}
	if _, err := s.Stat(t.Context(), mustKey(t, "logo.png")); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("a rejected Put published something: Stat = %v", err)
	}
	if got := d.entries(); len(got) != 0 {
		t.Errorf("a rejected Put left %v behind", got)
	}
}

// TestPutDoesNotReadFarPastTheDeclaredSize is the difference between a
// validation and a denial-of-service primitive.
//
// The source is wrapped in io.LimitReader(r, size+1), so a declaration of ten
// bytes cannot be used to make the store write ten gigabytes: it writes at most
// one byte more than declared and fails on that byte. That bound is why the
// store does not honour core/store.go's "Put reads r to EOF" on the failure
// path — draining an unbounded stream before rejecting it would turn a size
// check into an amplification primitive.
func TestPutDoesNotReadFarPastTheDeclaredSize(t *testing.T) {
	d := newTestDir(t)
	s := d.open()

	const declared = 10
	counter := &countingReader{r: bytes.NewReader(bytes.Repeat([]byte("g"), 10<<20))}

	_, err := s.Put(t.Context(), core.Asset{Key: mustKey(t, "logo.png"), Size: declared}, counter)
	if !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Put with an under-declared size = %v, want an error matching core.ErrInvalid", err)
	}
	if counter.read > declared+1 {
		t.Errorf("Put advanced the caller's reader by %d bytes, want at most %d", counter.read, declared+1)
	}
}

// TestPutStoresAZeroLengthObject asserts that zero is a length like any other.
func TestPutStoresAZeroLengthObject(t *testing.T) {
	d := newTestDir(t)
	s := d.open()

	ref := put(t, s, "empty.png", nil)
	if ref.Size != 0 {
		t.Errorf("Size = %d, want 0", ref.Size)
	}
	if ref.Digest != sha256Of(t, nil) {
		t.Errorf("Digest = %q, want the sha256 of no bytes", ref.Digest)
	}
	if got := d.raw("empty.png"); len(got) != 0 {
		t.Errorf("the file holds %d bytes, want none", len(got))
	}
}

// TestPutCreatesParentDirectories covers the ordinary case of a key with a
// prefix nobody has used yet.
func TestPutCreatesParentDirectories(t *testing.T) {
	d := newTestDir(t)
	s := d.open()

	put(t, s, "img/2026/08/hero.jpg", []byte("bytes"))

	for _, dir := range []string{"img", "img/2026", "img/2026/08"} {
		fi, err := os.Lstat(d.join(dir))
		if err != nil {
			t.Fatalf("lstat %s: %v", dir, err)
		}
		if !fi.IsDir() {
			t.Errorf("%s is not a directory", dir)
		}
	}
}

// TestPutOverwritesAnExistingObject asserts the last-writer-wins contract
// core/store.go states: assets have no compare-and-swap, because the content
// repository is where concurrent editing happens.
func TestPutOverwritesAnExistingObject(t *testing.T) {
	d := newTestDir(t)
	s := d.open()

	put(t, s, "logo.png", []byte("first"))
	ref := put(t, s, "logo.png", []byte("second"))

	if got := string(d.raw("logo.png")); got != "second" {
		t.Errorf("the file holds %q, want %q", got, "second")
	}
	if ref.Size != int64(len("second")) {
		t.Errorf("Size = %d, want %d", ref.Size, len("second"))
	}
	if got := d.entries(); len(got) != 1 {
		t.Errorf("overwriting left %v, want one entry", got)
	}
}

// TestPutOverADirectoryIsErrExists covers one direction of the divergence
// between a hierarchical namespace and an object store.
func TestPutOverADirectoryIsErrExists(t *testing.T) {
	d := newTestDir(t)
	d.mkdir("img/logo.png")
	s := d.open()

	_, err := s.Put(t.Context(), core.Asset{Key: mustKey(t, "img/logo.png"), Size: 3}, strings.NewReader("abc"))
	if !errors.Is(err, core.ErrExists) {
		t.Errorf("Put over a directory = %v, want an error matching core.ErrExists", err)
	}
	if errors.Is(err, core.ErrInvalid) {
		t.Errorf("Put over a directory also matched core.ErrInvalid; the request is well formed")
	}
}

// TestPutUnderAnObjectIsErrExists covers the other direction, and it is the
// case that catches ENOTDIR — which, measured, does not satisfy
// errors.Is(err, fs.ErrNotExist) and so cannot be classified by a not-exist
// check that happened to work for the missing-key case.
//
// The filesystem cannot hold both "img/logo.png" and "img/logo.png/thumb.png";
// an object store holds both happily. That is a genuine backend divergence and
// it is one of the things a shared contract test would have to be careful not
// to assume.
func TestPutUnderAnObjectIsErrExists(t *testing.T) {
	d := newTestDir(t)
	d.drop("img/logo.png", []byte("an object, not a directory"))
	s := d.open()

	for _, key := range []string{"img/logo.png/thumb.png", "img/logo.png/a/b.png"} {
		t.Run(key, func(t *testing.T) {
			_, err := s.Put(t.Context(), core.Asset{Key: mustKey(t, key), Size: 3}, strings.NewReader("abc"))
			if !errors.Is(err, core.ErrExists) {
				t.Errorf("Put under an object = %v, want an error matching core.ErrExists", err)
			}
		})
	}
	if got := string(d.raw("img/logo.png")); got != "an object, not a directory" {
		t.Errorf("the blocking object was modified: %q", got)
	}
}

// TestGetUnderAnObjectIsNotFound is the read-side half of the same case: there
// is no object there, so the answer is absence rather than a conflict.
func TestGetUnderAnObjectIsNotFound(t *testing.T) {
	d := newTestDir(t)
	d.drop("img/logo.png", []byte("an object"))
	s := d.open()

	key := mustKey(t, "img/logo.png/thumb.png")
	if _, err := s.Stat(t.Context(), key); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Stat under an object = %v, want an error matching core.ErrNotFound", err)
	}
	if _, err := s.Get(t.Context(), key); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Get under an object = %v, want an error matching core.ErrNotFound", err)
	}
}

// TestPutOverASymlinkReplacesTheLink asserts what publishing by rename actually
// does to a link at the key: it replaces the directory entry and does not write
// through it. The file on the other side is untouched, and the key afterwards
// holds an ordinary object.
func TestPutOverASymlinkReplacesTheLink(t *testing.T) {
	skipWithoutSymlinks(t)

	d := newTestDir(t)
	outside := d.outsideFile("SECRET-OUTSIDE", []byte("SECRET-OUTSIDE-THE-ROOT"))
	d.link("../outside/SECRET-OUTSIDE", "logo.png")
	s := d.open()

	put(t, s, "logo.png", []byte("new bytes"))

	if got, err := os.ReadFile(outside); err != nil || string(got) != "SECRET-OUTSIDE-THE-ROOT" {
		t.Errorf("the file outside the root is now %q (%v); a Put must not write through a link", got, err)
	}
	fi, err := os.Lstat(d.join("logo.png"))
	if err != nil {
		t.Fatalf("lstat the key: %v", err)
	}
	if !fi.Mode().IsRegular() {
		t.Errorf("the key is %v, want a regular file", fi.Mode())
	}
	if got := string(d.raw("logo.png")); got != "new bytes" {
		t.Errorf("the key holds %q, want %q", got, "new bytes")
	}
}

// TestPutValidatesTheAssetAndTheKey covers the refusals Put makes before it
// touches the filesystem.
func TestPutValidatesTheAssetAndTheKey(t *testing.T) {
	d := newTestDir(t)
	s := d.open()

	cases := map[string]struct {
		asset core.Asset
		body  io.Reader
	}{
		"no key":            {core.Asset{Size: 0}, strings.NewReader("")},
		"negative size":     {core.Asset{Key: mustKey(t, "logo.png"), Size: -2}, strings.NewReader("")},
		"bad content type":  {core.Asset{Key: mustKey(t, "logo.png"), Size: 0, ContentType: "image//png"}, strings.NewReader("")},
		"reserved name":     {core.Asset{Key: mustKey(t, "nul.png"), Size: 0}, strings.NewReader("")},
		"trailing space":    {core.Asset{Key: mustKey(t, "logo.png "), Size: 0}, strings.NewReader("")},
		"forbidden colon":   {core.Asset{Key: mustKey(t, "img/logo.png:ads"), Size: 0}, strings.NewReader("")},
		"nil reader":        {core.Asset{Key: mustKey(t, "logo.png"), Size: 0}, nil},
		"reserved and deep": {core.Asset{Key: mustKey(t, "img/con/logo.png"), Size: 0}, strings.NewReader("")},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := s.Put(t.Context(), tc.asset, tc.body)
			if !errors.Is(err, core.ErrInvalid) {
				t.Errorf("Put = %v, want an error matching core.ErrInvalid", err)
			}
		})
	}
	if got := d.entries(); len(got) != 0 {
		t.Errorf("a refused Put touched the directory: %v", got)
	}
}

// TestPutDiscardsTheDeclaredContentType is named for what it does rather than
// for what it wishes were true.
//
// This is the cost of deriving rather than storing, asserted rather than left
// in prose, so that anybody who disagrees has to delete a test whose name
// states the trade. The alternative — persist the caller's content type — needs
// somewhere to persist it that a file dropped in by hand would also have, and
// there is no such place that is both portable and reliable.
func TestPutDiscardsTheDeclaredContentType(t *testing.T) {
	d := newTestDir(t)
	s := d.open()

	ref, err := s.Put(t.Context(), core.Asset{
		Key:         mustKey(t, "logo.png"),
		Size:        3,
		ContentType: "application/x-custom",
	}, strings.NewReader("abc"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if ref.ContentType != "image/png" {
		t.Errorf("Put's ContentType = %q, want the derived %q", ref.ContentType, "image/png")
	}

	stated, err := s.Stat(t.Context(), ref.Key)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if stated.ContentType != "image/png" {
		t.Errorf("Stat's ContentType = %q, want the derived %q", stated.ContentType, "image/png")
	}
}

// TestFailedPutLeavesNoObjectAndNoTempFile asserts the cleanup on the ordinary
// failure path, including the temp file, which is the thing nobody would notice
// accumulating.
func TestFailedPutLeavesNoObjectAndNoTempFile(t *testing.T) {
	d := newTestDir(t)
	s := d.open()
	boom := errors.New("the upload died")

	_, err := s.Put(t.Context(), core.Asset{Key: mustKey(t, "img/logo.png"), Size: 100},
		&failingReader{body: bytes.Repeat([]byte("p"), 40), at: 40, err: boom})
	if !errors.Is(err, boom) {
		t.Fatalf("Put = %v, want the reader's own error", err)
	}

	// The parent directory is created before the temp file and is deliberately
	// not removed: pruning it would race a concurrent Put to a sibling key.
	want := []string{"img/"}
	if got := d.entries(); !slices.Equal(got, want) {
		t.Errorf("a failed Put left %v, want %v", got, want)
	}
}

// TestCancelledPutLeavesThePreviousObjectIntact is the guarantee that matters
// to somebody replacing a live asset: a cancelled replacement is not a lost
// object.
func TestCancelledPutLeavesThePreviousObjectIntact(t *testing.T) {
	d := newTestDir(t)
	s := d.open()
	put(t, s, "logo.png", []byte("the object that was already there"))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	_, err := s.Put(ctx, core.Asset{Key: mustKey(t, "logo.png"), Size: 1 << 20},
		&cancellingReader{body: bytes.Repeat([]byte("n"), 1<<20), at: 4096, cancel: cancel})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled Put = %v, want an error matching context.Canceled", err)
	}

	if got := string(d.raw("logo.png")); got != "the object that was already there" {
		t.Errorf("the previous object is now %q", got)
	}
	want := []string{"logo.png"}
	if got := d.entries(); !slices.Equal(got, want) {
		t.Errorf("a cancelled Put left %v, want %v", got, want)
	}
}

// TestPutHonoursCancellationMidStream covers the create case: a cancelled Put
// over nothing leaves nothing.
func TestPutHonoursCancellationMidStream(t *testing.T) {
	d := newTestDir(t)
	s := d.open()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	_, err := s.Put(ctx, core.Asset{Key: mustKey(t, "logo.png"), Size: 1 << 20},
		&cancellingReader{body: bytes.Repeat([]byte("n"), 1<<20), at: 4096, cancel: cancel})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled Put = %v, want an error matching context.Canceled", err)
	}
	if got := d.entries(); len(got) != 0 {
		t.Errorf("a cancelled Put left %v behind", got)
	}
}

// TestPutHonoursCancellationBeforeItStarts asserts a done context is reported
// without the directory being touched at all.
func TestPutHonoursCancellationBeforeItStarts(t *testing.T) {
	d := newTestDir(t)
	s := d.open()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := s.Put(ctx, core.Asset{Key: mustKey(t, "img/logo.png"), Size: 3}, strings.NewReader("abc"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Put with a cancelled context = %v, want an error matching context.Canceled", err)
	}
	if got := d.entries(); len(got) != 0 {
		t.Errorf("a refused Put left %v behind", got)
	}
}

// TestPutObjectsAreOrdinaryFilesUnderTheUmask states the mode an object gets,
// and the consequence worth knowing rather than discovering: because an object
// is published by renaming a new file over the old one, an operator's chmod is
// reverted by the next Put. Writing in place would preserve the mode and is not
// an option, because it is not atomic.
func TestPutObjectsAreOrdinaryFilesUnderTheUmask(t *testing.T) {
	d := newTestDir(t)
	s := d.open()
	put(t, s, "logo.png", []byte("bytes"))

	if err := os.Chmod(d.join("logo.png"), 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	put(t, s, "logo.png", []byte("replaced"))

	fi, err := os.Lstat(d.join("logo.png"))
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if got := fi.Mode().Perm(); got == 0o600 {
		t.Errorf("mode is still %v: a Put publishes a new file, so it cannot preserve the old one's mode", got)
	}
	t.Logf("an object written under this process's umask has mode %v", fi.Mode().Perm())
}

// TestPutReportsTheReadersOwnFailure asserts that a caller's error reaches the
// caller rather than being flattened into a sentinel that would say the store
// was at fault.
func TestPutReportsTheReadersOwnFailure(t *testing.T) {
	d := newTestDir(t)
	s := d.open()
	boom := fmt.Errorf("upstream gave up")

	_, err := s.Put(t.Context(), core.Asset{Key: mustKey(t, "logo.png"), Size: core.SizeUnknown},
		&failingReader{body: []byte("some"), at: 4, err: boom})
	if !errors.Is(err, boom) {
		t.Errorf("Put = %v, want the reader's own error", err)
	}
	for _, sentinel := range []error{core.ErrNotFound, core.ErrInvalid, core.ErrExists, core.ErrConflict} {
		if errors.Is(err, sentinel) {
			t.Errorf("a reader's failure was classified as %v", sentinel)
		}
	}
}

// TestPutOverANonRegularEntryReplacesItWithoutOpeningIt closes the last corner
// of the FIFO hazard.
//
// Put never opens the key — it writes a temporary file and renames over the
// name — so a FIFO at the key cannot block it the way an unguarded Get would.
// The watchdog is what says that is a property rather than an accident, and the
// rename leaves an ordinary object where the trap was.
func TestPutOverANonRegularEntryReplacesItWithoutOpeningIt(t *testing.T) {
	kinds := map[string]func(t *testing.T, d *testDir){
		"fifo": func(t *testing.T, d *testDir) {
			skipWithoutFifo(t)
			d.fifo("entry.png")
		},
		"socket": func(t *testing.T, d *testDir) {
			skipWithoutSocket(t, d)
			d.socket("entry.png")
		},
		"symlink": func(t *testing.T, d *testDir) {
			skipWithoutSymlinks(t)
			d.drop("target.png", []byte("a real file"))
			d.link("target.png", "entry.png")
		},
	}

	for kind, plant := range kinds {
		t.Run(kind, func(t *testing.T) {
			d := newTestDir(t)
			plant(t, d)
			s := d.open()

			done := make(chan error, 1)
			go func() {
				_, err := s.Put(context.WithoutCancel(t.Context()),
					core.Asset{Key: mustKey(t, "entry.png"), Size: 9}, strings.NewReader("new bytes"))
				done <- err
			}()

			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("Put over a %s = %v, want it to replace the entry", kind, err)
				}
			case <-time.After(watchdog):
				t.Fatalf("Put over a %s blocked for %v: Put must never open the key", kind, watchdog)
			}

			fi, err := os.Lstat(d.join("entry.png"))
			if err != nil {
				t.Fatalf("lstat the key: %v", err)
			}
			if !fi.Mode().IsRegular() {
				t.Errorf("the key is %v, want a regular file", fi.Mode())
			}
			if got := string(d.raw("entry.png")); got != "new bytes" {
				t.Errorf("the key holds %q, want the replacement", got)
			}
		})
	}
}
