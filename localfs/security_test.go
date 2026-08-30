package localfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shutx-net/micawber/core"
)

// What is planted outside the root. Every assertion in this file that says "no
// path leaked" also says "these bytes did not leak", which is the failure a
// path leak would actually enable.
//
// decoyName is what an escaping key points at, and is deliberately a name that
// looks like an ordinary asset: an error is allowed to echo the key the caller
// supplied — core's own ValidationError documents that as safe, since a key is
// content addressing and never a credential — so a leak check that forbade the
// key would be checking the wrong thing.
const (
	secretName = "SECRET-OUTSIDE"
	secretBody = "SECRET-OUTSIDE-THE-ROOT"
	decoyName  = "hidden.png"
	decoyBody  = "ALSO-OUTSIDE-THE-ROOT"
)

// escapeCase is one way of pointing at something outside the root from a name
// inside it.
type escapeCase struct {
	name  string
	key   string
	plant func(d *testDir, outside string)
}

// escapeCases is the matrix. All three are planted by an operator or an
// attacker who can write into the directory, which is the only way any of them
// can exist.
var escapeCases = []escapeCase{
	{
		name: "relative symlink at the key",
		key:  "escape.png",
		plant: func(d *testDir, _ string) {
			d.link("../outside/"+secretName, "escape.png")
		},
	},
	{
		name: "absolute symlink at the key",
		key:  "escape.png",
		plant: func(d *testDir, outside string) {
			d.link(outside, "escape.png")
		},
	},
	{
		name: "symlinked intermediate directory",
		key:  "dirlink/" + decoyName,
		plant: func(d *testDir, _ string) {
			d.link("../outside", "dirlink")
		},
	},
}

// plantOutside fills the sibling directory and returns what it holds, so that a
// later check can say the store neither read anything into existence there nor
// changed or removed anything.
func plantOutside(t *testing.T, d *testDir) map[string]string {
	t.Helper()

	d.outsideFile(secretName, []byte(secretBody))
	d.outsideFile(decoyName, []byte(decoyBody))
	return map[string]string{secretName: secretBody, decoyName: decoyBody}
}

// newEscapeDir plants one case and returns the store to try it against.
func newEscapeDir(t *testing.T, c escapeCase) (*testDir, *Store) {
	t.Helper()
	skipWithoutSymlinks(t)

	d := newTestDir(t)
	plantOutside(t, d)
	c.plant(d, filepath.Join(d.outside, secretName))
	return d, d.open()
}

// TestSymlinkInsideTheRootCannotWriteOutsideIt is the write half, stated on its
// own because it is the one with lasting consequences.
func TestSymlinkInsideTheRootCannotWriteOutsideIt(t *testing.T) {
	skipWithoutSymlinks(t)

	d := newTestDir(t)
	want := plantOutside(t, d)
	d.link("../outside", "dirlink")
	s := d.open()

	_, err := s.Put(t.Context(), core.Asset{Key: mustKey(t, "dirlink/planted.png"), Size: 5}, strings.NewReader("bytes"))
	if err == nil {
		t.Fatalf("a Put through an escaping directory link succeeded")
	}
	if !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Put through an escaping directory link = %v, want an error matching core.ErrNotFound", err)
	}
	assertOutsideMatches(t, d, want)
}

// TestSymlinkedIntermediateDirectoryCannotEscape covers the read half of the
// same shape: os.Root resolves each component with the check and the open
// fused, so a link partway along a name is refused exactly as one at the end is.
func TestSymlinkedIntermediateDirectoryCannotEscape(t *testing.T) {
	skipWithoutSymlinks(t)

	d := newTestDir(t)
	plantOutside(t, d)
	d.link("../outside", "dirlink")
	s := d.open()

	r, err := s.Get(t.Context(), mustKey(t, "dirlink/"+decoyName))
	if err == nil {
		b, _ := io.ReadAll(r)
		_ = r.Close()
		t.Fatalf("a Get through an escaping directory link returned %q", b)
	}
	if !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Get through an escaping directory link = %v, want an error matching core.ErrNotFound", err)
	}
}

// TestAbsoluteSymlinkIsRefused covers the link that does not even try to look
// relative. os.Root rejects an absolute target outright rather than resolving
// it and then checking, which is why it cannot be raced.
func TestAbsoluteSymlinkIsRefused(t *testing.T) {
	skipWithoutSymlinks(t)

	d := newTestDir(t)
	plantOutside(t, d)
	d.link(filepath.Join(d.outside, secretName), "escape.png")
	d.link("/etc/passwd", "passwd.png")
	s := d.open()

	for _, name := range []string{"escape.png", "passwd.png"} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Stat(t.Context(), mustKey(t, name)); !errors.Is(err, core.ErrNotFound) {
				t.Errorf("Stat(%q) = %v, want an error matching core.ErrNotFound", name, err)
			}
		})
	}
}

// TestDotDotCannotAppearInAKeyAtAll asserts that traversal never reaches this
// package: core.NewAssetKey refuses a ".." segment, an absolute key, a
// backslash and an uncleaned path, so the store's own defences are for the
// filesystem's tricks rather than for the caller's.
func TestDotDotCannotAppearInAKeyAtAll(t *testing.T) {
	keys := []string{
		"../outside/" + secretName,
		"img/../../outside/" + secretName,
		"..",
		"/etc/passwd",
		`..\outside`,
		"img/./logo.png",
		"img//logo.png",
	}

	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			if _, err := core.NewAssetKey(key); !errors.Is(err, core.ErrInvalid) {
				t.Errorf("core.NewAssetKey(%q) = %v, want an error matching core.ErrInvalid", key, err)
			}
		})
	}
}

// TestEveryOperationRefusesAnEscapingKeyIdentically is the sweep.
//
// Get, Stat and Delete against each of the three escapes: nine cases, every one
// of them core.ErrNotFound, every one of them indistinguishable in class from a
// plain missing key, and the file outside the root untouched afterwards. Put is
// covered separately, because a rename over a link at the key replaces the link
// rather than writing through it, which is a different — and also safe —
// outcome.
func TestEveryOperationRefusesAnEscapingKeyIdentically(t *testing.T) {
	for _, c := range escapeCases {
		for _, op := range []string{"get", "stat", "delete"} {
			t.Run(c.name+"/"+op, func(t *testing.T) {
				d, s := newEscapeDir(t, c)
				key := mustKey(t, c.key)

				var err error
				switch op {
				case "get":
					var r io.ReadCloser
					r, err = s.Get(t.Context(), key)
					if r != nil {
						b, _ := io.ReadAll(r)
						_ = r.Close()
						t.Fatalf("%s returned a reader over %q", op, b)
					}
				case "stat":
					_, err = s.Stat(t.Context(), key)
				case "delete":
					err = s.Delete(t.Context(), key)
				}

				if !errors.Is(err, core.ErrNotFound) {
					t.Errorf("%s = %v, want an error matching core.ErrNotFound", op, err)
				}
				assertIndistinguishableFromMissing(t, s, err)
				assertOutsideMatches(t, d, map[string]string{secretName: secretBody, decoyName: decoyBody})
				assertNoPathLeak(t, d, err)
			})
		}

		t.Run(c.name+"/put", func(t *testing.T) {
			d, s := newEscapeDir(t, c)
			_, err := s.Put(t.Context(), core.Asset{Key: mustKey(t, c.key), Size: 5}, strings.NewReader("bytes"))
			if err != nil {
				assertNoPathLeak(t, d, err)
			}
			assertOutsideMatches(t, d, map[string]string{secretName: secretBody, decoyName: decoyBody})
		})
	}
}

// assertIndistinguishableFromMissing is the property the escape rule is
// actually about: a caller must not be able to tell an escape from an absence,
// because the difference is information about the filesystem outside the root.
func assertIndistinguishableFromMissing(t *testing.T, s *Store, err error) {
	t.Helper()

	_, missing := s.Stat(t.Context(), mustKey(t, "definitely-not-there.png"))
	for _, sentinel := range []error{core.ErrNotFound, core.ErrInvalid, core.ErrExists, core.ErrConflict, core.ErrUnsupported} {
		want := errors.Is(missing, sentinel)
		if got := errors.Is(err, sentinel); got != want {
			t.Errorf("%v classifies differently from a missing key against %v: %v, want %v", err, sentinel, got, want)
		}
	}
}

// assertOutsideMatches checks that nothing on the other side of a link was
// created, modified or removed.
func assertOutsideMatches(t *testing.T, d *testDir, want map[string]string) {
	t.Helper()

	entries, err := os.ReadDir(d.outside)
	if err != nil {
		t.Fatalf("read the directory outside the root: %v", err)
	}
	got := make(map[string]string, len(entries))
	for _, entry := range entries {
		body, err := os.ReadFile(filepath.Join(d.outside, entry.Name()))
		if err != nil {
			t.Fatalf("read %s outside the root: %v", entry.Name(), err)
		}
		got[entry.Name()] = string(body)
	}
	if !maps.Equal(got, want) {
		t.Errorf("the directory outside the root now holds %v, want %v", got, want)
	}
}

// TestHardLinkToAFileOutsideTheRootIsServedAndDocumented is the uncomfortable
// one, and it asserts the measured truth rather than a wish.
//
// A hard link into the directory IS readable through the store, because a hard
// link is not a reference to a file, it is the file: there is no path-based
// defence, and the only signal is a link count, which is not portable and which
// would also refuse an operator de-duplicating two objects. Creating one needs
// write access to the directory, which the store's trust precondition already
// places outside the boundary — anyone who can create entries there can write
// any bytes there anyway.
//
// The test exists so that this is a recorded property rather than a discovery,
// and so that anybody who later claims the store confines reads to the
// directory has to change a test that says otherwise.
func TestHardLinkToAFileOutsideTheRootIsServedAndDocumented(t *testing.T) {
	d := newTestDir(t)
	plantOutside(t, d)
	d.hardlink(filepath.Join(d.outside, secretName), "innocent.png")
	s := d.open()

	r, err := s.Get(t.Context(), mustKey(t, "innocent.png"))
	if err != nil {
		t.Fatalf("Get of a hard link: %v; the documented behaviour is that it is served", err)
	}
	defer func() { _ = r.Close() }()

	body, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(body) != secretBody {
		t.Errorf("read %q, want %q: a hard link is indistinguishable from a regular file", body, secretBody)
	}
	t.Logf("recorded: a hard link into the directory is served, because os.Root does not prohibit it and no portable check can")
}

// TestPutOverAHardLinkDoesNotWriteThroughIt is the other half, and it is the
// one that bounds the exposure: publishing by rename replaces the directory
// entry, so a hard link is a disclosure risk on a read and never a corruption
// risk on a write.
func TestPutOverAHardLinkDoesNotWriteThroughIt(t *testing.T) {
	d := newTestDir(t)
	want := plantOutside(t, d)
	outside := filepath.Join(d.outside, secretName)
	d.hardlink(outside, "innocent.png")
	s := d.open()

	put(t, s, "innocent.png", []byte("replacement bytes"))

	assertOutsideMatches(t, d, want)
	if got, err := os.ReadFile(outside); err != nil || string(got) != secretBody {
		t.Errorf("the file outside the root is now %q (%v), want %q", got, err, secretBody)
	}
	if got := string(d.raw("innocent.png")); got != "replacement bytes" {
		t.Errorf("the key holds %q, want the replacement", got)
	}
}

// failingOperations runs every way this package has of failing on a key and
// hands each error to fn. It is the input to both leak assertions.
func failingOperations(t *testing.T, d *testDir, s *Store, fn func(label string, err error)) {
	t.Helper()

	run := func(label string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s: expected a failure and got none", label)
			return
		}
		fn(label, err)
	}

	key := func(name string) core.AssetKey { return mustKey(t, name) }

	_, err := s.Stat(t.Context(), key("missing.png"))
	run("stat a missing key", err)

	_, err = s.Get(t.Context(), key("missing.png"))
	run("get a missing key", err)

	run("delete a missing key", s.Delete(t.Context(), key("missing.png")))

	_, err = s.Stat(t.Context(), key("adir"))
	run("stat a directory", err)

	_, err = s.Get(t.Context(), key("adir"))
	run("get a directory", err)

	run("delete a directory", s.Delete(t.Context(), key("adir")))

	_, err = s.Stat(t.Context(), key("blocker.png/under.png"))
	run("stat under an object", err)

	_, err = s.Put(t.Context(), core.Asset{Key: key("blocker.png/under.png"), Size: 1}, strings.NewReader("x"))
	run("put under an object", err)

	_, err = s.Put(t.Context(), core.Asset{Key: key("adir"), Size: 1}, strings.NewReader("x"))
	run("put over a directory", err)

	_, err = s.Put(t.Context(), core.Asset{Key: key("declared.png"), Size: 99}, strings.NewReader("short"))
	run("put with a size disagreement", err)

	_, err = s.Stat(t.Context(), key("nul.png"))
	run("stat a key this store refuses", err)

	// A reader that fails partway: the error travels back through Put.
	_, err = s.Put(t.Context(), core.Asset{Key: key("failing.png"), Size: core.SizeUnknown},
		&failingReader{body: []byte("some"), at: 4, err: fmt.Errorf("the upload died")})
	run("put whose reader fails", err)

	// An error raised by an already-open file, which is the measured trap: the
	// message an *os.File builds carries its own name, the joined absolute path.
	if f, err := os.Open(d.join("adir")); err == nil {
		_ = f.Close()
	}
	if r, err := s.Get(t.Context(), key("readable.png")); err == nil {
		_ = r.Close()
		_, readErr := r.Read(make([]byte, 1))
		run("read from a closed reader", readErr)
	}
}

// plantFailures builds the directory every failing operation above needs.
func plantFailures(t *testing.T) (*testDir, *Store) {
	t.Helper()

	d := newTestDir(t)
	plantOutside(t, d)
	d.mkdir("adir")
	d.drop("blocker.png", []byte("an object, not a directory"))
	d.drop("nul.png", []byte("unreachable"))
	d.drop("readable.png", []byte("ordinary"))
	return d, d.open()
}

// TestErrorsDoNotLeakAPath is the executable form of the rule.
//
// It is not a check of one message. Every failing operation the package has on
// a key is run, and each error is asserted against the root's own absolute
// path, its base name, the temporary directory above it, and the name and
// contents of a file planted outside. The measured trap it exists to catch is
// that an error raised by an already-open *os.File carries os.File.Name(),
// which is the joined absolute path — so the store extracts the underlying
// condition and builds its own message around the key rather than wrapping the
// *os.PathError it was given.
func TestErrorsDoNotLeakAPath(t *testing.T) {
	d, s := plantFailures(t)

	failingOperations(t, d, s, func(label string, err error) {
		t.Helper()
		assertNoPathLeakLabelled(t, d, label, err)
	})
}

// TestErrorsDoNotLeakASymlinkTarget covers the same rule for the value os.Root
// would happily hand over: Readlink returns "../outside" and "/etc/passwd"
// verbatim, so this package never calls it.
func TestErrorsDoNotLeakASymlinkTarget(t *testing.T) {
	skipWithoutSymlinks(t)

	d := newTestDir(t)
	plantOutside(t, d)
	d.link("../outside/"+secretName, "a.png")
	d.link("/etc/shadow", "b.png")
	d.link("nowhere-at-all", "c.png")
	s := d.open()

	targets := []string{"../outside/" + secretName, secretName, "/etc/shadow", "/etc/", "nowhere-at-all"}
	for _, name := range []string{"a.png", "b.png", "c.png"} {
		t.Run(name, func(t *testing.T) {
			_, err := s.Stat(t.Context(), mustKey(t, name))
			if err == nil {
				t.Fatalf("Stat(%q) succeeded", name)
			}
			for _, target := range targets {
				if strings.Contains(err.Error(), target) {
					t.Errorf("the error names a symlink target %q: %v", target, err)
				}
			}
		})
	}
}

// assertNoPathLeak checks one error against everything the store must never
// say.
func assertNoPathLeak(t *testing.T, d *testDir, err error) {
	t.Helper()
	assertNoPathLeakLabelled(t, d, "operation", err)
}

func assertNoPathLeakLabelled(t *testing.T, d *testDir, label string, err error) {
	t.Helper()

	msg := err.Error()
	forbidden := map[string]string{
		"the root's absolute path":   d.path,
		"the directory above it":     filepath.Dir(d.path),
		"the directory outside it":   d.outside,
		"the name planted outside":   secretName,
		"the bytes planted outside":  secretBody,
		"a joined path to an object": d.join("readable.png"),
	}
	for what, needle := range forbidden {
		if needle == "" {
			continue
		}
		if strings.Contains(msg, needle) {
			t.Errorf("%s: the error names %s (%q): %v", label, what, needle, err)
		}
	}
}

// TestUnreadableDirectoryIsNotAFourHundred asserts that a permission problem is
// not classified as a caller mistake.
//
// Without skipIfRoot this would pass vacuously in a root container, because
// root bypasses the chmod entirely: the operation would succeed, no error would
// appear, and an assertion of the form "the error is not one of these" would be
// true of nothing.
func TestUnreadableDirectoryIsNotAFourHundred(t *testing.T) {
	skipIfRoot(t)

	d := newTestDir(t)
	plantOutside(t, d)
	d.drop("locked/logo.png", []byte("bytes"))
	s := d.open()

	if err := os.Chmod(d.join("locked"), 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(d.join("locked"), 0o755) })

	_, err := s.Stat(t.Context(), mustKey(t, "locked/logo.png"))
	if err == nil {
		t.Fatalf("Stat inside an unreadable directory succeeded")
	}
	for _, sentinel := range []error{core.ErrNotFound, core.ErrInvalid, core.ErrExists, core.ErrConflict, core.ErrUnsupported} {
		if errors.Is(err, sentinel) {
			t.Errorf("a permission failure was classified as %v: %v", sentinel, err)
		}
	}
	assertNoPathLeak(t, d, err)
}

// TestReservedNamesAreRefusedOnThisHostToo asserts the platform-independence
// claim on the platform where it is not necessary.
//
// Measured, os.Root.Create succeeds for every one of these on Linux and they
// appear on disk as ordinary files. Without a rule of its own, a Linux instance
// would accept keys a Windows instance cannot, and an asset referenced from a
// document in Git would resolve on one machine and not the other.
func TestReservedNamesAreRefusedOnThisHostToo(t *testing.T) {
	d := newTestDir(t)
	s := d.open()

	for _, name := range []string{"nul.png", "NUL", "con", "com1.jpg", "aux", "lpt1", "logo.png.", "logo.png ", "  ", "img/nul/logo.png"} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			key := mustKey(t, name)

			_, err := s.Put(t.Context(), core.Asset{Key: key, Size: 1}, strings.NewReader("x"))
			if !errors.Is(err, core.ErrInvalid) {
				t.Errorf("Put(%q) = %v, want an error matching core.ErrInvalid", name, err)
			}
			if _, err := s.Stat(t.Context(), key); !errors.Is(err, core.ErrInvalid) {
				t.Errorf("Stat(%q) = %v, want an error matching core.ErrInvalid", name, err)
			}
		})
	}
	if got := d.entries(); len(got) != 0 {
		t.Errorf("a refused key still reached the filesystem: %v", got)
	}
}

// TestStoreRefusesNothingItShouldAccept is the counterweight to every rule in
// this file. A store that refused everything would pass the whole escape
// matrix.
func TestStoreRefusesNothingItShouldAccept(t *testing.T) {
	d := newTestDir(t)
	s := d.open()

	keys := []string{
		"logo.png",
		"img/2026/08/hero.jpg",
		"a.b.c.png",
		"console.png",
		"lpt10.png",
		"-leading-dash.png",
		"file with spaces.png",
		"日本語/写真.jpg",
		"emoji-🙂.png",
		"deep/a/b/c/d/e/f/g/h/i/j/k.png",
		strings.Repeat("x", 200) + "/" + strings.Repeat("y", 200) + ".png",
	}

	for _, name := range keys {
		t.Run(name, func(t *testing.T) {
			key := mustKey(t, name)
			body := []byte("round trip " + name)

			ref, err := s.Put(t.Context(), core.Asset{Key: key, Size: int64(len(body))}, strings.NewReader(string(body)))
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			if err := ref.Validate(); err != nil {
				t.Errorf("the ref does not validate: %v", err)
			}
			if _, err := s.Stat(t.Context(), key); err != nil {
				t.Errorf("Stat: %v", err)
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
			if string(got) != string(body) {
				t.Errorf("read %q, want %q", got, body)
			}
			if err := s.Delete(t.Context(), key); err != nil {
				t.Errorf("Delete: %v", err)
			}
		})
	}
}

// TestNoOperationTouchesANonRegularEntry sweeps the whole not-a-regular-file
// family across every operation in one table, so that a new entry kind added
// later has an obvious place to go and an obvious answer to give.
//
// The socket is the one that needs a host check rather than a platform check:
// binding one under t.TempDir() can fail because sun_path is shorter than the
// temporary directory's own name, which is a property of where the suite ran
// and not of the store.
func TestNoOperationTouchesANonRegularEntry(t *testing.T) {
	kinds := map[string]func(t *testing.T, d *testDir){
		"directory": func(t *testing.T, d *testDir) { d.mkdir("entry.png") },
		"symlink": func(t *testing.T, d *testDir) {
			skipWithoutSymlinks(t)
			d.drop("target.png", []byte("a real file"))
			d.link("target.png", "entry.png")
		},
		"fifo": func(t *testing.T, d *testDir) {
			skipWithoutFifo(t)
			d.fifo("entry.png")
		},
		"socket": func(t *testing.T, d *testDir) {
			skipWithoutSocket(t, d)
			d.socket("entry.png")
		},
	}

	for kind, plant := range kinds {
		t.Run(kind, func(t *testing.T) {
			d := newTestDir(t)
			plant(t, d)
			s := d.open()
			key := mustKey(t, "entry.png")

			done := make(chan error, 3)
			go func() {
				ctx := context.WithoutCancel(t.Context())
				_, err := s.Stat(ctx, key)
				done <- err
				r, err := s.Get(ctx, key)
				if r != nil {
					_ = r.Close()
				}
				done <- err
				done <- s.Delete(ctx, key)
			}()

			for _, op := range []string{"stat", "get", "delete"} {
				select {
				case err := <-done:
					if !errors.Is(err, core.ErrNotFound) {
						t.Errorf("%s of a %s = %v, want an error matching core.ErrNotFound", op, kind, err)
					}
				case <-time.After(watchdog):
					t.Fatalf("%s of a %s blocked for %v: the regular-files-only rule is what stops an open waiting forever", op, kind, watchdog)
				}
			}

			if _, err := os.Lstat(d.join("entry.png")); err != nil {
				t.Errorf("the %s was removed: %v", kind, err)
			}
		})
	}
}
