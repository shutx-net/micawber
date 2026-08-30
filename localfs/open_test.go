package localfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/shutx-net/micawber/core"
)

// TestStoreSatisfiesTheCoreInterface says in a form a reader can grep for what
// the compile-time assertion in store.go says to the compiler.
func TestStoreSatisfiesTheCoreInterface(t *testing.T) {
	var s any = (*Store)(nil)
	if _, ok := s.(core.AssetStore); !ok {
		t.Fatalf("*Store does not satisfy core.AssetStore")
	}
}

// TestOpenRequiresAnExistingDirectory asserts that Open creates nothing.
// Writing into a directory somebody did not mean to create is the wrong default
// for a tool that manages their content, and it is the same call the git
// adapter makes about running git init.
func TestOpenRequiresAnExistingDirectory(t *testing.T) {
	d := newTestDir(t)
	missing := filepath.Join(d.path, "not-created-by-open")

	if _, err := Open(t.Context(), missing); err == nil {
		t.Fatalf("Open of a missing directory succeeded")
	}
	if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Open created the directory it was given: %v", err)
	}
}

// TestOpenRejectsAFile covers the other way the argument can be wrong.
func TestOpenRejectsAFile(t *testing.T) {
	d := newTestDir(t)
	d.drop("notadir", []byte("x"))

	if _, err := Open(t.Context(), d.join("notadir")); err == nil {
		t.Fatalf("Open of a regular file succeeded")
	}
}

// TestOpenRejectsAnEmptyDirectoryName covers the argument nobody means to pass.
func TestOpenRejectsAnEmptyDirectoryName(t *testing.T) {
	for _, dir := range []string{"", "   "} {
		if _, err := Open(t.Context(), dir); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("Open(%q) = %v, want an error matching core.ErrInvalid", dir, err)
		}
	}
}

// TestOpenRejectsANilOption keeps a nil in a slice of options from becoming a
// panic on the first call instead of an error at construction.
func TestOpenRejectsANilOption(t *testing.T) {
	d := newTestDir(t)

	if _, err := Open(t.Context(), d.path, nil); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("Open with a nil Option = %v, want an error matching core.ErrInvalid", err)
	}
}

// TestOpenCreatesNothing is the assertion that stops anybody adding a probe or
// a marker file later. Open performs no case-sensitivity test and writes no
// index, because a probe means writing into the operator's directory as a side
// effect of opening it.
func TestOpenCreatesNothing(t *testing.T) {
	d := newTestDir(t)
	before := d.entries()

	s := d.open()
	if got := d.entries(); len(got) != len(before) {
		t.Errorf("Open changed the directory from %v to %v", before, got)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := d.entries(); len(got) != len(before) {
		t.Errorf("Close changed the directory from %v to %v", before, got)
	}
}

// TestOpenHonoursCancellation asserts that an already-cancelled context is
// reported before any descriptor is taken.
func TestOpenHonoursCancellation(t *testing.T) {
	d := newTestDir(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := Open(ctx, d.path); !errors.Is(err, context.Canceled) {
		t.Errorf("Open with a cancelled context = %v, want an error matching context.Canceled", err)
	}
}

// TestCloseReleasesTheRoot states the store's whole resource footprint: one
// descriptor for the root, and nothing pooled, queued or reference-counted.
func TestCloseReleasesTheRoot(t *testing.T) {
	d := newTestDir(t)

	before := openDescriptors(t)
	stores := make([]*Store, 0, 20)
	for range 20 {
		s, err := Open(t.Context(), d.path)
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		stores = append(stores, s)
	}
	if held := openDescriptors(t) - before; held != len(stores) {
		t.Errorf("%d open stores hold %d descriptors, want %d", len(stores), held, len(stores))
	}
	for _, s := range stores {
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	if after := openDescriptors(t); after != before {
		t.Errorf("descriptor count is %d after closing every store, want %d", after, before)
	}
}

// TestAStoreIsUnusableAfterClose asserts the honest failure: every operation
// reports an error, and none of them reports it as a caller mistake.
func TestAStoreIsUnusableAfterClose(t *testing.T) {
	d := newTestDir(t)
	d.drop("logo.png", []byte("x"))

	s, err := Open(t.Context(), d.path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	key := mustKey(t, "logo.png")
	if _, err := s.Stat(t.Context(), key); err == nil {
		t.Errorf("Stat on a closed store succeeded")
	}
	if _, err := s.Get(t.Context(), key); err == nil {
		t.Errorf("Get on a closed store succeeded")
	}
	if err := s.Delete(t.Context(), key); err == nil {
		t.Errorf("Delete on a closed store succeeded")
	}
}
