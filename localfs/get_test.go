package localfs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/shutx-net/micawber/core"
)

// TestGetStreamsTheBytesExactly is the byte-fidelity assertion: a store adds
// nothing to what it was given and takes nothing away.
func TestGetStreamsTheBytesExactly(t *testing.T) {
	d := newTestDir(t)
	body := bytes.Repeat([]byte("abcdefghij\x00\xff"), 40_000)
	d.drop("img/photo.jpg", body)
	s := d.open()

	r, err := s.Get(t.Context(), mustKey(t, "img/photo.jpg"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = r.Close() }()

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("read %d bytes, want the %d written", len(got), len(body))
	}
	if err := r.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// TestGetOfAZeroLengthObject asserts an empty stream rather than an error.
func TestGetOfAZeroLengthObject(t *testing.T) {
	d := newTestDir(t)
	d.drop("empty.png", nil)
	s := d.open()

	r, err := s.Get(t.Context(), mustKey(t, "empty.png"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = r.Close() }()

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("read %d bytes, want none", len(got))
	}
}

// TestGetOfAMissingKeyIsNotFound covers absence and an absent ancestor.
func TestGetOfAMissingKeyIsNotFound(t *testing.T) {
	d := newTestDir(t)
	s := d.open()

	for _, name := range []string{"missing.png", "no/such/dir/logo.png"} {
		t.Run(name, func(t *testing.T) {
			r, err := s.Get(t.Context(), mustKey(t, name))
			if !errors.Is(err, core.ErrNotFound) {
				t.Errorf("Get(%q) = %v, want an error matching core.ErrNotFound", name, err)
			}
			if r != nil {
				t.Errorf("Get(%q) returned a reader alongside an error", name)
			}
		})
	}
}

// TestGetOfADirectoryIsNotFound covers the entry that would otherwise open
// successfully and then fail mid-stream with EISDIR, which is a much worse
// failure than a refusal: a caller would already have started a response.
func TestGetOfADirectoryIsNotFound(t *testing.T) {
	d := newTestDir(t)
	d.mkdir("img")
	s := d.open()

	if _, err := s.Get(t.Context(), mustKey(t, "img")); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Get of a directory = %v, want an error matching core.ErrNotFound", err)
	}
}

// TestGetOfASymlinkIsNotFoundWhereverItPoints asserts the same three cases Stat
// asserts, on the path that would actually return bytes.
func TestGetOfASymlinkIsNotFoundWhereverItPoints(t *testing.T) {
	skipWithoutSymlinks(t)

	d := newTestDir(t)
	d.drop("real.png", []byte("inside the root"))
	d.outsideFile("SECRET-OUTSIDE", []byte("SECRET-OUTSIDE-THE-ROOT"))
	d.link("real.png", "inside.png")
	d.link("../outside/SECRET-OUTSIDE", "escaping.png")
	d.link("nowhere.png", "dangling.png")
	s := d.open()

	for _, name := range []string{"inside.png", "escaping.png", "dangling.png"} {
		t.Run(name, func(t *testing.T) {
			r, err := s.Get(t.Context(), mustKey(t, name))
			if err == nil {
				b, _ := io.ReadAll(r)
				_ = r.Close()
				t.Fatalf("Get(%q) succeeded and returned %q", name, b)
			}
			if !errors.Is(err, core.ErrNotFound) {
				t.Errorf("Get(%q) = %v, want an error matching core.ErrNotFound", name, err)
			}
		})
	}
}

// TestGetRefusesAFifoWithoutOpeningIt is the test that must not be allowed to
// hang the package.
//
// open(2) on a FIFO with no writer blocks until one appears, and a context
// cannot interrupt a blocked open, so one mkfifo in an asset directory would
// hang a request thread permanently. The watchdog is what turns a regression
// from "the suite stops responding until the go test timeout" into a failure
// with a message.
func TestGetRefusesAFifoWithoutOpeningIt(t *testing.T) {
	skipWithoutFifo(t)

	d := newTestDir(t)
	d.fifo("trap.png")
	s := d.open()

	type result struct {
		r   io.ReadCloser
		err error
	}
	done := make(chan result, 1)
	go func() {
		r, err := s.Get(context.WithoutCancel(t.Context()), mustKey(t, "trap.png"))
		done <- result{r, err}
	}()

	select {
	case got := <-done:
		if got.r != nil {
			_ = got.r.Close()
		}
		if !errors.Is(got.err, core.ErrNotFound) {
			t.Errorf("Get of a FIFO = %v, want an error matching core.ErrNotFound", got.err)
		}
	case <-time.After(watchdog):
		t.Fatalf("Get of a FIFO blocked for %v: the regular-files-only rule is what stops open(2) waiting for a writer forever", watchdog)
	}
}

// TestGetStopsWhenTheContextIsCancelledMidStream asserts that cancellation
// means something on a long stream, and says what granularity it has.
//
// The reader carries the context Get was given and returns its error from Read,
// so a cancelled stream stops within one io.Copy buffer — 32 KiB — rather than
// at the end of the object. No goroutine watches the context: one per open
// object would leak for every caller who forgets Close, turning a bounded
// descriptor leak into an unbounded goroutine leak, and would make Close racy
// with an in-flight Read. Cancellation stops the stream; only Close frees the
// descriptor.
func TestGetStopsWhenTheContextIsCancelledMidStream(t *testing.T) {
	d := newTestDir(t)
	const size = 4 << 20
	d.drop("big.png", bytes.Repeat([]byte("x"), size))
	s := d.open()

	ctx, cancel := context.WithCancel(t.Context())
	r, err := s.Get(ctx, mustKey(t, "big.png"))
	if err != nil {
		cancel()
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = r.Close() }()

	head := make([]byte, 1024)
	if _, err := io.ReadFull(r, head); err != nil {
		cancel()
		t.Fatalf("read the first chunk: %v", err)
	}

	cancel()

	rest, err := io.Copy(io.Discard, r)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("reading after cancellation = %v, want an error matching context.Canceled", err)
	}
	if total := int64(len(head)) + rest; total > size/2 {
		t.Errorf("read %d of %d bytes after cancelling, want it to stop well short", total, size)
	}
	if err := r.Close(); err != nil {
		t.Errorf("Close after cancellation: %v", err)
	}
}

// TestGetReaderSurvivesTheStoreBeingClosed asserts that a response already
// streaming is not broken by shutdown. The descriptor is independent of the
// root's once the file is open, and Store.Close closes the root and not the
// readers already handed out.
func TestGetReaderSurvivesTheStoreBeingClosed(t *testing.T) {
	d := newTestDir(t)
	body := bytes.Repeat([]byte("streaming"), 10_000)
	d.drop("logo.png", body)

	s, err := Open(t.Context(), d.path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	r, err := s.Get(t.Context(), mustKey(t, "logo.png"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = r.Close() }()

	head := make([]byte, 9)
	if _, err := io.ReadFull(r, head); err != nil {
		t.Fatalf("read the first chunk: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close the store: %v", err)
	}

	rest, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read after the store closed: %v", err)
	}
	if got := append(head, rest...); !bytes.Equal(got, body) {
		t.Errorf("read %d bytes across the store closing, want the %d written", len(got), len(body))
	}
}

// TestGetDoesNotLeakDescriptors is the executable form of the store's resource
// statement: one descriptor per open reader, released by Close, and nothing
// pooled or reference-counted behind it.
func TestGetDoesNotLeakDescriptors(t *testing.T) {
	d := newTestDir(t)
	d.drop("logo.png", []byte("bytes"))
	s := d.open()
	key := mustKey(t, "logo.png")

	before := openDescriptors(t)

	readers := make([]io.ReadCloser, 0, 20)
	for range 20 {
		r, err := s.Get(t.Context(), key)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		readers = append(readers, r)
	}
	if held := openDescriptors(t) - before; held != len(readers) {
		t.Errorf("%d open readers hold %d descriptors, want %d", len(readers), held, len(readers))
	}
	for _, r := range readers {
		if err := r.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	if after := openDescriptors(t); after != before {
		t.Errorf("descriptor count is %d after closing every reader, want %d", after, before)
	}
}

// TestGetOfALargeObjectDoesNotBufferIt asserts that Get streams rather than
// copies.
//
// The proof is that bytes written into the same inode after Get returned are
// visible to the reader: a Get that had read the object into memory would hand
// back the old bytes. That matters beyond tidiness — a buffering Get would make
// serving a large asset cost its size in memory, and would be the obvious way
// for somebody to later "add" a digest to Stat.
func TestGetOfALargeObjectDoesNotBufferIt(t *testing.T) {
	d := newTestDir(t)
	const size = 1 << 20
	const offset = size / 2
	d.drop("big.png", bytes.Repeat([]byte("a"), size))
	s := d.open()

	before := openDescriptors(t)
	r, err := s.Get(t.Context(), mustKey(t, "big.png"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = r.Close() }()
	if held := openDescriptors(t) - before; held != 1 {
		t.Errorf("Get holds %d descriptors, want exactly 1", held)
	}

	f, err := os.OpenFile(d.join("big.png"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("reopen the object for writing: %v", err)
	}
	if _, err := f.WriteAt([]byte("ZZZZ"), offset); err != nil {
		t.Fatalf("write into the object: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close the writer: %v", err)
	}

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != size {
		t.Fatalf("read %d bytes, want %d", len(got), size)
	}
	if string(got[offset:offset+4]) != "ZZZZ" {
		t.Errorf("the reader returned bytes captured at Get time; Get must stream, not buffer")
	}
}

// TestGetValidatesTheKey asserts this store's added key rules on the read path.
func TestGetValidatesTheKey(t *testing.T) {
	d := newTestDir(t)
	d.drop("nul.png", []byte("unreachable"))
	s := d.open()

	if _, err := s.Get(t.Context(), mustKey(t, "nul.png")); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("Get of a reserved name = %v, want an error matching core.ErrInvalid", err)
	}
}

// TestGetHonoursCancellationBeforeItOpens asserts that a done context is
// reported without taking a descriptor.
func TestGetHonoursCancellationBeforeItOpens(t *testing.T) {
	d := newTestDir(t)
	d.drop("logo.png", []byte("x"))
	s := d.open()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	before := openDescriptors(t)
	if _, err := s.Get(ctx, mustKey(t, "logo.png")); !errors.Is(err, context.Canceled) {
		t.Errorf("Get with a cancelled context = %v, want an error matching context.Canceled", err)
	}
	if after := openDescriptors(t); after != before {
		t.Errorf("a refused Get changed the descriptor count from %d to %d", before, after)
	}
}
