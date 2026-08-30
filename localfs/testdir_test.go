package localfs

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"

	"github.com/shutx-net/micawber/core"
)

// testDir is a throwaway directory the store is opened on, plus a sibling the
// store must never reach.
//
// Both live under one t.TempDir(), so a relative "../outside/x" symlink planted
// inside the root points at something real: an escape test that pointed at a
// path which did not exist would pass for the wrong reason.
//
// Every helper here touches the filesystem with the os package directly, never
// through the Store. That is what lets a test assert what the store did rather
// than assert the store against itself, and it is what makes a file dropped in
// by hand genuinely dropped in by hand.
type testDir struct {
	t *testing.T
	// path is the directory the Store is opened on.
	path string
	// outside is a sibling directory, reachable from path only by escaping it.
	outside string
}

// newTestDir creates the root and its sibling under t.TempDir().
func newTestDir(t *testing.T) *testDir {
	t.Helper()

	base := t.TempDir()
	d := &testDir{
		t:       t,
		path:    filepath.Join(base, "root"),
		outside: filepath.Join(base, "outside"),
	}
	for _, dir := range []string{d.path, d.outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}
	return d
}

// join turns a slash-separated name relative to the root into a path for the os
// package.
func (d *testDir) join(name string) string {
	return filepath.Join(d.path, filepath.FromSlash(name))
}

// drop writes a file into the root without going through the Store, creating
// any parent directories it needs.
func (d *testDir) drop(name string, b []byte) {
	d.t.Helper()

	full := d.join(name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		d.t.Fatalf("create parents of %s: %v", name, err)
	}
	if err := os.WriteFile(full, b, 0o644); err != nil {
		d.t.Fatalf("write %s: %v", name, err)
	}
}

// raw reads a file back out of the root without going through the Store.
func (d *testDir) raw(name string) []byte {
	d.t.Helper()

	b, err := os.ReadFile(d.join(name))
	if err != nil {
		d.t.Fatalf("read %s: %v", name, err)
	}
	return b
}

// mkdir creates a directory inside the root.
func (d *testDir) mkdir(name string) {
	d.t.Helper()

	if err := os.MkdirAll(d.join(name), 0o755); err != nil {
		d.t.Fatalf("mkdir %s: %v", name, err)
	}
}

// link plants a symbolic link at name pointing at target, which is written into
// the link verbatim and so may be relative, absolute or dangling.
func (d *testDir) link(target, name string) {
	d.t.Helper()

	full := d.join(name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		d.t.Fatalf("create parents of %s: %v", name, err)
	}
	if err := os.Symlink(target, full); err != nil {
		d.t.Fatalf("symlink %s -> %s: %v", name, target, err)
	}
}

// hardlink plants a hard link at name to the existing file at target, which is
// an absolute path and is usually outside the root.
func (d *testDir) hardlink(target, name string) {
	d.t.Helper()

	full := d.join(name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		d.t.Fatalf("create parents of %s: %v", name, err)
	}
	if err := os.Link(target, full); err != nil {
		d.t.Fatalf("hard link %s -> %s: %v", name, target, err)
	}
}

// fifo creates a named pipe inside the root. Nothing ever opens it: opening a
// FIFO with no writer blocks, which is the whole reason the store refuses one.
func (d *testDir) fifo(name string) {
	d.t.Helper()

	full := d.join(name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		d.t.Fatalf("create parents of %s: %v", name, err)
	}
	if err := syscall.Mkfifo(full, 0o644); err != nil {
		d.t.Fatalf("mkfifo %s: %v", name, err)
	}
}

// socket binds a Unix domain socket inside the root. Like the FIFO, nothing
// ever connects to it: it is there to be refused.
func (d *testDir) socket(name string) {
	d.t.Helper()

	if err := d.bindSocket(name); err != nil {
		d.t.Fatalf("bind %s: %v", name, err)
	}
}

// bindSocket is the raw attempt, shared by the helper above and by the skip
// check, so that the check and the real thing bind a path of the same shape.
func (d *testDir) bindSocket(name string) error {
	full := d.join(name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return err
	}
	defer func() { _ = syscall.Close(fd) }()
	return syscall.Bind(fd, &syscall.SockaddrUnix{Name: full})
}

// outsideFile writes a file into the sibling directory and returns its absolute
// path, for a test that needs something real on the other side of a link.
func (d *testDir) outsideFile(name string, b []byte) string {
	d.t.Helper()

	full := filepath.Join(d.outside, name)
	if err := os.WriteFile(full, b, 0o644); err != nil {
		d.t.Fatalf("write %s: %v", full, err)
	}
	return full
}

// entries lists everything in the root, recursively, as sorted slash-separated
// names. A directory is reported with a trailing slash so that a listing states
// what kind of thing each entry is.
func (d *testDir) entries() []string {
	d.t.Helper()

	var names []string
	err := filepath.WalkDir(d.path, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(d.path, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		name := filepath.ToSlash(rel)
		if entry.IsDir() {
			name += "/"
		}
		names = append(names, name)
		return nil
	})
	if err != nil {
		d.t.Fatalf("walk %s: %v", d.path, err)
	}
	slices.Sort(names)
	return names
}

// open returns a Store on the root, closed when the test ends.
func (d *testDir) open(opts ...Option) *Store {
	d.t.Helper()

	s, err := Open(d.t.Context(), d.path, opts...)
	if err != nil {
		d.t.Fatalf("open store: %v", err)
	}
	d.t.Cleanup(func() { _ = s.Close() })
	return s
}

// skipIfRoot skips a test that works by making something unreadable.
//
// root bypasses discretionary access checks, so the chmod does not stop the
// operation, no error appears, and the test either fails for a reason that is
// not a defect or passes without asserting anything. Skipping says so.
func skipIfRoot(t *testing.T) {
	t.Helper()

	if os.Geteuid() == 0 {
		t.Skip("running as root: a permission test cannot mean anything when the process bypasses permission checks")
	}
}

// skipWithoutSymlinks skips when this host will not create a symbolic link,
// which on Windows needs a privilege or developer mode.
func skipWithoutSymlinks(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	if err := os.Symlink("target", filepath.Join(dir, "link")); err != nil {
		t.Skipf("this host will not create symbolic links (%v), so the escape matrix cannot be planted", err)
	}
}

// skipWithoutFifo skips when this host will not create a named pipe.
func skipWithoutFifo(t *testing.T) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "probe")
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Skipf("this host will not create named pipes (%v), so the blocking-open hazard cannot be planted", err)
	}
}

// skipWithoutSocket skips when a Unix domain socket cannot be bound inside this
// test's own directory.
//
// It probes in place rather than somewhere convenient, because the usual reason
// for failure is not that the host lacks sockets: sun_path is 108 bytes, and a
// t.TempDir() named after a long test can leave less than that. A probe
// elsewhere would answer a question the test was not asking.
func skipWithoutSocket(t *testing.T, d *testDir) {
	t.Helper()

	const probe = "probe.socket"
	if err := d.bindSocket(probe); err != nil {
		t.Skipf("a Unix domain socket cannot be bound in this test's directory (%v); sun_path is 108 bytes and this path is longer", err)
	}
	if err := os.Remove(d.join(probe)); err != nil {
		t.Fatalf("remove the probe socket: %v", err)
	}
}

// mustKey builds an asset key or fails the test. Every test here is about the
// store rather than about core's validation, which has its own suite.
func mustKey(t *testing.T, s string) core.AssetKey {
	t.Helper()

	k, err := core.NewAssetKey(s)
	if err != nil {
		t.Fatalf("new asset key %q: %v", s, err)
	}
	return k
}

// openDescriptors counts this process's open file descriptors, or skips the
// test on a host that will not say.
//
// A leaking caller's failure mode is EMFILE on some later unrelated operation
// rather than anything the store can report, so counting is the only way to
// state the descriptor discipline as an assertion instead of a promise.
func openDescriptors(t *testing.T) int {
	t.Helper()

	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("this host does not expose /proc/self/fd (%v), so descriptor accounting cannot be asserted", err)
	}
	// The ReadDir call holds a descriptor of its own while it runs, and it is
	// closed by the time the count is compared, so only the difference between
	// two counts taken this way is meaningful.
	return len(entries)
}
