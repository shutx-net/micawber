package localfs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/shutx-net/micawber/core"
)

// The shape of the racing-Put scenario, taken from the measurement the plan
// carries: eight writers, sixty Puts each, four readers looping over the whole
// object.
const (
	racingWriters   = 8
	putsPerWriter   = 60
	racingReaders   = 4
	racingObjectLen = 256 << 10
)

// filler is the body one writer Puts: a single byte repeated, so that a torn
// read is a mixture and therefore obvious.
func filler(b byte) []byte {
	return bytes.Repeat([]byte{b}, racingObjectLen)
}

// singleByte reports the byte a body is made of, and false when it is a
// mixture — which is what a torn read looks like.
func singleByte(b []byte) (byte, bool) {
	if len(b) == 0 {
		return 0, false
	}
	for _, c := range b {
		if c != b[0] {
			return 0, false
		}
	}
	return b[0], true
}

// TestConcurrentPutsToOneKeyNeverTear is the measured scenario turned into a
// test, and it is what says the atomicity is real rather than intended.
//
// Every completed read must be one repeated byte over the full length. A
// mixture would mean a reader saw a half-written object, which is exactly what
// writing in place instead of renaming would produce. The counts are reported
// rather than asserted, because how many reads a run gets through is a property
// of the machine and not of the store.
func TestConcurrentPutsToOneKeyNeverTear(t *testing.T) {
	d := newTestDir(t)
	s := d.open()
	key := mustKey(t, "logo.png")

	// One object must exist before the readers start, or they would spend the
	// race reporting a missing key rather than reading.
	put(t, s, "logo.png", filler('a'))

	var writers, readers sync.WaitGroup
	stop := make(chan struct{})

	var mu sync.Mutex
	var puts, reads, torn, notFound int

	for w := range racingWriters {
		writers.Add(1)
		go func() {
			defer writers.Done()
			body := filler(byte('A' + w))
			for range putsPerWriter {
				_, err := s.Put(t.Context(), core.Asset{Key: key, Size: int64(len(body))}, bytes.NewReader(body))
				mu.Lock()
				if err != nil {
					t.Errorf("Put: %v", err)
				}
				puts++
				mu.Unlock()
			}
		}()
	}

	for range racingReaders {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				r, err := s.Get(t.Context(), key)
				if err != nil {
					mu.Lock()
					notFound++
					mu.Unlock()
					continue
				}
				body, readErr := io.ReadAll(r)
				closeErr := r.Close()

				mu.Lock()
				switch {
				case readErr != nil:
					t.Errorf("read: %v", readErr)
				case closeErr != nil:
					t.Errorf("close: %v", closeErr)
				default:
					if _, uniform := singleByte(body); !uniform || len(body) != racingObjectLen {
						torn++
					} else {
						reads++
					}
				}
				mu.Unlock()
			}
		}()
	}

	writers.Wait()
	close(stop)
	readers.Wait()

	mu.Lock()
	defer mu.Unlock()
	t.Logf("observed: %d Puts, %d whole-object reads, %d torn, %d not-found", puts, reads, torn, notFound)
	if torn != 0 {
		t.Errorf("%d reads saw a partial object; publishing by rename is what makes that impossible", torn)
	}
	if reads == 0 {
		t.Errorf("no reader completed a whole object, so the torn-read assertion measured nothing")
	}
	if notFound != 0 {
		t.Errorf("%d reads found no object; a rename never leaves the key absent", notFound)
	}

	want := []string{"logo.png"}
	if got := d.entries(); !slices.Equal(got, want) {
		t.Errorf("after the race the directory holds %v, want %v: every temporary file must have been published or removed", got, want)
	}
}

// TestConcurrentPutsToOneKeyAllReturnTheirOwnRef asserts the consequence of
// building the ref from the temporary file rather than from the key.
//
// Every Put returns a digest of the bytes THAT Put wrote, even though only one
// of them is at the key afterwards. A ref built from a stat after the rename
// fails this test, which is exactly why it is written: it would be a Put
// reporting bytes it did not write.
func TestConcurrentPutsToOneKeyAllReturnTheirOwnRef(t *testing.T) {
	d := newTestDir(t)
	s := d.open()
	key := mustKey(t, "logo.png")

	const writers = 8
	const each = 20

	type observation struct {
		wanted core.Digest
		got    core.Digest
		size   int64
	}
	results := make(chan observation, writers*each)

	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				body := bytes.Repeat([]byte(fmt.Sprintf("%d-%d;", w, i)), 500)
				sum := sha256.Sum256(body)
				wanted, err := core.NewDigest("sha256", hex.EncodeToString(sum[:]))
				if err != nil {
					t.Errorf("build the expected digest: %v", err)
					return
				}
				ref, err := s.Put(t.Context(), core.Asset{Key: key, Size: int64(len(body))}, bytes.NewReader(body))
				if err != nil {
					t.Errorf("Put: %v", err)
					return
				}
				results <- observation{wanted: wanted, got: ref.Digest, size: ref.Size}
				if ref.Size != int64(len(body)) {
					t.Errorf("Size = %d, want %d", ref.Size, len(body))
				}
			}
		}()
	}
	wg.Wait()
	close(results)

	wrong := 0
	total := 0
	for obs := range results {
		total++
		if obs.got != obs.wanted {
			wrong++
		}
	}
	t.Logf("observed: %d concurrent Puts to one key, %d refs describing another writer's bytes", total, wrong)
	if wrong != 0 {
		t.Errorf("%d of %d Puts returned a ref for bytes they did not write", wrong, total)
	}

	// Exactly one object, and it is one of the bodies rather than a mixture.
	want := []string{"logo.png"}
	if got := d.entries(); !slices.Equal(got, want) {
		t.Errorf("after the race the directory holds %v, want %v", got, want)
	}
}

// TestPutWhileGetIsStreamingLeavesTheReaderOnTheOldObject asserts the POSIX
// property that makes it safe to serve a large asset over a slow connection
// while an editor replaces it: rename replaces the directory entry, not the
// inode, so an open descriptor keeps the object it opened.
func TestPutWhileGetIsStreamingLeavesTheReaderOnTheOldObject(t *testing.T) {
	d := newTestDir(t)
	s := d.open()
	key := mustKey(t, "logo.png")

	old := []byte(strings.Repeat("OLD-BYTES;", 20_000))
	fresh := []byte(strings.Repeat("NEW-BYTES;", 20_000))
	put(t, s, "logo.png", old)

	r, err := s.Get(t.Context(), key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = r.Close() }()

	head := make([]byte, 10)
	if _, err := io.ReadFull(r, head); err != nil {
		t.Fatalf("read the first chunk: %v", err)
	}

	put(t, s, "logo.png", fresh)

	rest, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read after the replacement: %v", err)
	}
	if got := append(head, rest...); !bytes.Equal(got, old) {
		t.Errorf("the reader saw %d bytes that are not the object it opened", len(got))
	}
	t.Logf("observed: the reader completed the old object across a Put; a fresh Get sees the new one")

	if got := string(d.raw("logo.png")); got != string(fresh) {
		t.Errorf("the key does not hold the new object")
	}
}

// TestDeleteWhileGetIsStreamingLeavesTheReaderIntact asserts the other POSIX
// property: unlink removes the entry and the inode survives until the last
// descriptor closes. The consequence worth knowing is that a forgotten Close
// pins the disk blocks as well as the descriptor.
func TestDeleteWhileGetIsStreamingLeavesTheReaderIntact(t *testing.T) {
	d := newTestDir(t)
	s := d.open()
	key := mustKey(t, "logo.png")

	body := []byte(strings.Repeat("still-readable;", 20_000))
	put(t, s, "logo.png", body)

	r, err := s.Get(t.Context(), key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = r.Close() }()

	head := make([]byte, 15)
	if _, err := io.ReadFull(r, head); err != nil {
		t.Fatalf("read the first chunk: %v", err)
	}

	if err := s.Delete(t.Context(), key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Stat(t.Context(), key); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Stat after the Delete = %v, want an error matching core.ErrNotFound", err)
	}

	rest, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read after the delete: %v", err)
	}
	if got := append(head, rest...); !bytes.Equal(got, body) {
		t.Errorf("the reader saw %d bytes, want the %d it opened", len(got), len(body))
	}
	t.Logf("observed: the reader completed the object after it was deleted, and Stat reported it absent immediately")
}

// TestConcurrentPutsToSiblingKeysUnderANewPrefix asserts that two writers
// creating the same parent do not have to coordinate: MkdirAll is idempotent
// under EEXIST.
func TestConcurrentPutsToSiblingKeysUnderANewPrefix(t *testing.T) {
	d := newTestDir(t)
	s := d.open()

	const writers = 16
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key := mustKey(t, fmt.Sprintf("img/2026/08/%02d.png", w))
			body := []byte(fmt.Sprintf("object %d", w))
			if _, err := s.Put(t.Context(), core.Asset{Key: key, Size: int64(len(body))}, bytes.NewReader(body)); err != nil {
				t.Errorf("Put %v: %v", key, err)
			}
		}()
	}
	wg.Wait()

	for w := range writers {
		name := fmt.Sprintf("img/2026/08/%02d.png", w)
		if got, want := string(d.raw(name)), fmt.Sprintf("object %d", w); got != want {
			t.Errorf("%s holds %q, want %q", name, got, want)
		}
	}
	t.Logf("observed: %d concurrent Puts under one new prefix, all succeeded, no coordination", writers)
}

// TestConcurrentDeletesOfOneKeyProduceOneSuccess is the property that makes
// Delete's non-idempotence usable rather than surprising: exactly one caller is
// told it removed the object.
func TestConcurrentDeletesOfOneKeyProduceOneSuccess(t *testing.T) {
	d := newTestDir(t)
	s := d.open()
	key := mustKey(t, "logo.png")
	put(t, s, "logo.png", []byte("bytes"))

	const callers = 16
	errs := make(chan error, callers)
	var start sync.WaitGroup
	start.Add(1)

	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start.Wait()
			errs <- s.Delete(t.Context(), key)
		}()
	}
	start.Done()
	wg.Wait()
	close(errs)

	succeeded, notFound, other := 0, 0, 0
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, core.ErrNotFound):
			notFound++
		default:
			other++
			t.Errorf("Delete = %v, want nil or an error matching core.ErrNotFound", err)
		}
	}
	t.Logf("observed: %d concurrent Deletes, %d succeeded, %d reported not-found, %d reported something else", callers, succeeded, notFound, other)
	if succeeded != 1 {
		t.Errorf("%d Deletes succeeded, want exactly 1", succeeded)
	}
	if got := d.entries(); len(got) != 0 {
		t.Errorf("after the race the directory holds %v, want nothing", got)
	}
}

// TestAStoreIsSafeForConcurrentUse runs every operation against one Store from
// many goroutines at once. Under -race it is what says the Store's own state is
// what the documentation claims it is: one root descriptor and one read-only
// map, and nothing else.
func TestAStoreIsSafeForConcurrentUse(t *testing.T) {
	d := newTestDir(t)
	s := d.open()

	const workers = 16
	const rounds = 25

	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key := mustKey(t, fmt.Sprintf("img/%02d/logo.png", w%4))
			for i := range rounds {
				body := []byte(fmt.Sprintf("worker %d round %d", w, i))
				switch i % 4 {
				case 0:
					if _, err := s.Put(t.Context(), core.Asset{Key: key, Size: int64(len(body))}, bytes.NewReader(body)); err != nil {
						t.Errorf("Put: %v", err)
					}
				case 1:
					if _, err := s.Stat(t.Context(), key); err != nil && !errors.Is(err, core.ErrNotFound) {
						t.Errorf("Stat: %v", err)
					}
				case 2:
					r, err := s.Get(t.Context(), key)
					if err != nil {
						if !errors.Is(err, core.ErrNotFound) {
							t.Errorf("Get: %v", err)
						}
						continue
					}
					if _, err := io.ReadAll(r); err != nil {
						t.Errorf("read: %v", err)
					}
					if err := r.Close(); err != nil {
						t.Errorf("Close: %v", err)
					}
				case 3:
					if err := s.Delete(t.Context(), key); err != nil && !errors.Is(err, core.ErrNotFound) {
						t.Errorf("Delete: %v", err)
					}
				}
			}
		}()
	}
	wg.Wait()

	for _, name := range d.entries() {
		if strings.Contains(name, tempPrefix) {
			t.Errorf("a temporary file survived the race: %s", name)
		}
	}
	t.Logf("observed: %d goroutines x %d mixed operations on one Store, directory left holding %v", workers, rounds, d.entries())
}
