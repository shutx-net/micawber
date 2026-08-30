package localfs

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/shutx-net/micawber/core"
)

// TestDeleteRemovesTheObject is the ordinary case, checked with the os package
// so that the store is not asked to confirm its own work.
func TestDeleteRemovesTheObject(t *testing.T) {
	d := newTestDir(t)
	s := d.open()
	put(t, s, "img/logo.png", []byte("bytes"))

	if err := s.Delete(t.Context(), mustKey(t, "img/logo.png")); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Lstat(d.join("img/logo.png")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the object is still there: %v", err)
	}
}

// TestDeleteOfAHandDroppedFileWorks asserts there is no such thing as an object
// the store did not write. There is no ownership record, so there is nothing to
// check before removing one.
func TestDeleteOfAHandDroppedFileWorks(t *testing.T) {
	d := newTestDir(t)
	d.drop("photos/hero.jpg", []byte("dropped in by hand"))
	s := d.open()

	if err := s.Delete(t.Context(), mustKey(t, "photos/hero.jpg")); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Lstat(d.join("photos/hero.jpg")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the object is still there: %v", err)
	}
}

// TestDeleteOfAMissingKeyIsNotFound and TestDeleteIsNotIdempotent together
// cover what core/store.go asks for: deleting something that is not there is an
// error matching ErrNotFound, matching ContentRepository.Delete.
func TestDeleteOfAMissingKeyIsNotFound(t *testing.T) {
	d := newTestDir(t)
	s := d.open()

	for _, name := range []string{"missing.png", "no/such/dir/logo.png"} {
		t.Run(name, func(t *testing.T) {
			if err := s.Delete(t.Context(), mustKey(t, name)); !errors.Is(err, core.ErrNotFound) {
				t.Errorf("Delete(%q) = %v, want an error matching core.ErrNotFound", name, err)
			}
		})
	}
}

// TestDeleteIsNotIdempotent states the contract in the direction a caller is
// most likely to be surprised by, so that a later "tidy-up" has to change a
// test that says what it is for.
func TestDeleteIsNotIdempotent(t *testing.T) {
	d := newTestDir(t)
	s := d.open()
	put(t, s, "logo.png", []byte("bytes"))

	if err := s.Delete(t.Context(), mustKey(t, "logo.png")); err != nil {
		t.Fatalf("the first Delete: %v", err)
	}
	if err := s.Delete(t.Context(), mustKey(t, "logo.png")); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("the second Delete = %v, want an error matching core.ErrNotFound", err)
	}
}

// TestDeleteRefusesToRemoveADirectory covers a directory with something in it.
func TestDeleteRefusesToRemoveADirectory(t *testing.T) {
	d := newTestDir(t)
	d.drop("img/logo.png", []byte("bytes"))
	s := d.open()

	if err := s.Delete(t.Context(), mustKey(t, "img")); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Delete of a directory = %v, want an error matching core.ErrNotFound", err)
	}
	if _, err := os.Lstat(d.join("img")); err != nil {
		t.Errorf("the directory is gone: %v", err)
	}
}

// TestDeleteRefusesToRemoveAnEmptyDirectory is the one that would not exist
// without reading os/root_unix.go.
//
// Root.Remove is unlinkat(fd, name, 0) with a retry at AT_REMOVEDIR, so it
// removes an empty directory — confirmed by probe — and without the explicit
// check before it, a Delete of a key that happened to name one would silently
// succeed and remove it. The residual race is benign and bounded: a directory
// created at exactly that path between the check and the removal is lost, and
// there is no portable unlink-without-AT_REMOVEDIR in the standard library, so
// it cannot be made atomic. What is lost is an empty directory, and the
// alternative is no check at all.
func TestDeleteRefusesToRemoveAnEmptyDirectory(t *testing.T) {
	d := newTestDir(t)
	d.mkdir("img/2026/08")
	s := d.open()

	if err := s.Delete(t.Context(), mustKey(t, "img/2026/08")); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Delete of an empty directory = %v, want an error matching core.ErrNotFound", err)
	}
	fi, err := os.Lstat(d.join("img/2026/08"))
	if err != nil {
		t.Fatalf("the empty directory was removed: %v", err)
	}
	if !fi.IsDir() {
		t.Errorf("img/2026/08 is %v, want a directory", fi.Mode())
	}
}

// TestDeleteRefusesToRemoveASymlink covers all three targets, so that Delete
// cannot become a way to unlink something the store would not read.
func TestDeleteRefusesToRemoveASymlink(t *testing.T) {
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
			if err := s.Delete(t.Context(), mustKey(t, name)); !errors.Is(err, core.ErrNotFound) {
				t.Errorf("Delete(%q) = %v, want an error matching core.ErrNotFound", name, err)
			}
			if _, err := os.Lstat(d.join(name)); err != nil {
				t.Errorf("the link is gone: %v", err)
			}
		})
	}
	if got := string(d.raw("real.png")); got != "inside the root" {
		t.Errorf("the link's target inside the root was disturbed: %q", got)
	}
}

// TestDeleteRefusesToRemoveAFifo covers the last of the not-a-regular-file
// family on the removal path.
func TestDeleteRefusesToRemoveAFifo(t *testing.T) {
	skipWithoutFifo(t)

	d := newTestDir(t)
	d.fifo("trap.png")
	s := d.open()

	if err := s.Delete(t.Context(), mustKey(t, "trap.png")); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Delete of a FIFO = %v, want an error matching core.ErrNotFound", err)
	}
	if _, err := os.Lstat(d.join("trap.png")); err != nil {
		t.Errorf("the FIFO is gone: %v", err)
	}
}

// TestDeleteUnderAnObjectIsNotFound completes the pair with the Put case: a key
// whose ancestor is a regular file is ErrExists on the way in, because
// something is in the way, and ErrNotFound on the way out, because there is no
// object there.
func TestDeleteUnderAnObjectIsNotFound(t *testing.T) {
	d := newTestDir(t)
	d.drop("img/logo.png", []byte("an object"))
	s := d.open()

	if err := s.Delete(t.Context(), mustKey(t, "img/logo.png/thumb.png")); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Delete under an object = %v, want an error matching core.ErrNotFound", err)
	}
}

// TestDeleteLeavesEmptyParentDirectories is named for the behaviour so that a
// later tidy-up has to delete a test that says what it is for.
//
// Pruning empty parents was considered and rejected on a concrete race: walking
// up from img/2026/08 can remove a directory a concurrent Put has just created
// and not yet renamed into, which turns the other writer's rename into ENOENT
// and fails a Put that should have succeeded. Empty directories are inert, the
// operator can see and remove them, and they are not a correctness problem.
func TestDeleteLeavesEmptyParentDirectories(t *testing.T) {
	d := newTestDir(t)
	s := d.open()
	put(t, s, "img/2026/08/x.png", []byte("bytes"))

	if err := s.Delete(t.Context(), mustKey(t, "img/2026/08/x.png")); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	want := []string{"img/", "img/2026/", "img/2026/08/"}
	if got := d.entries(); !slices.Equal(got, want) {
		t.Errorf("after a Delete the directory holds %v, want %v", got, want)
	}
}

// TestDeleteHonoursCancellation asserts a done context is reported before
// anything is unlinked.
func TestDeleteHonoursCancellation(t *testing.T) {
	d := newTestDir(t)
	s := d.open()
	put(t, s, "logo.png", []byte("bytes"))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := s.Delete(ctx, mustKey(t, "logo.png")); !errors.Is(err, context.Canceled) {
		t.Errorf("Delete with a cancelled context = %v, want an error matching context.Canceled", err)
	}
	if _, err := os.Lstat(d.join("logo.png")); err != nil {
		t.Errorf("a cancelled Delete removed the object: %v", err)
	}
}

// TestDeleteValidatesTheKey asserts this store's added key rules on the removal
// path, so that the set of addressable keys is one set on every operation.
func TestDeleteValidatesTheKey(t *testing.T) {
	d := newTestDir(t)
	d.drop("nul.png", []byte("unreachable"))
	s := d.open()

	if err := s.Delete(t.Context(), mustKey(t, "nul.png")); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("Delete of a reserved name = %v, want an error matching core.ErrInvalid", err)
	}
	if _, err := os.Lstat(d.join("nul.png")); err != nil {
		t.Errorf("a refused Delete removed the file: %v", err)
	}
}
