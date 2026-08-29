package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/shutx-net/micawber/core"
)

// objectID is a Git object name in lowercase hex, exactly as git prints it: 40
// characters in a SHA-1 repository and 64 in a SHA-256 one.
//
// It is the type a [core.Revision] from this package is made of. Keeping it
// distinct from a plain string is what makes "this came out of git" visible at
// the places an object id is put back into an argument vector.
type objectID string

// blobType and treeType are the object types cat-file reports that this package
// has an opinion about. A "commit" entry in a tree is a submodule, and anything
// else is not something a document could be.
const (
	blobType = "blob"
	treeType = "tree"
)

// valid reports whether id looks like an object name git printed: lowercase hex
// of one of the two lengths Git uses.
//
// Every id this package puts into an argument vector has been through here, so
// output that was mis-parsed becomes an error rather than an argument.
func (id objectID) valid() bool {
	if len(id) != 40 && len(id) != 64 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// zero returns the all-zero object id of the same width as id, which is how
// update-index spells "remove this entry".
func (id objectID) zero() objectID {
	width := len(id)
	if width != 40 && width != 64 {
		width = 40
	}
	return objectID(strings.Repeat("0", width))
}

// batchEntry is one response from "git cat-file --batch": the object it resolved
// to and the bytes it holds, or the fact that there is no such object.
type batchEntry struct {
	// oid is the object the spec resolved to, empty when it did not.
	oid objectID
	// typ is git's word for what the object is: "blob", "tree", "commit", "tag".
	typ string
	// data is the object's contents, copied out of the response buffer so the
	// caller may keep and mutate it.
	data []byte
	// missing reports that git found no such object.
	missing bool
}

// catFile resolves and reads every spec in one "git cat-file --batch", and
// returns the responses in the order the specs were given.
//
// One process does the whole job: a batch fed "<ref>:<path>" resolves the object
// name and streams the object in the same spawn, which is why a Get costs one
// process where a rev-parse followed by a cat-file would cost two. The specs
// travel on standard input, so no path this package reads ever becomes an
// argument.
func (r *Repository) catFile(ctx context.Context, specs ...string) ([]batchEntry, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	var stdin bytes.Buffer
	for _, spec := range specs {
		// A newline would end the request early and turn the remainder into a
		// second one. core rejects control characters in a path and Open
		// validates the ref, so this cannot happen; if it ever does, it must not
		// happen silently.
		if strings.ContainsAny(spec, "\n\x00") {
			return nil, sentinelf(core.ErrInvalid, nil, "object spec contains a line break")
		}
		stdin.WriteString(spec)
		stdin.WriteByte('\n')
	}

	out, err := r.exec.run(ctx, invocation{args: []string{"cat-file", "--batch"}, stdin: stdin.Bytes()})
	if err != nil {
		return nil, err
	}
	return parseBatch(out, len(specs))
}

// parseBatch splits a cat-file --batch response into one entry per request.
//
// The framing is "<oid> <type> <size>\n<size bytes>\n" for an object git has and
// "<request> missing\n" for one it does not. The size field is authoritative and
// the payload is taken by length rather than by looking for the newline, because
// a document may legitimately end with one.
func parseBatch(out []byte, want int) ([]batchEntry, error) {
	entries := make([]batchEntry, 0, want)
	rest := out

	for len(entries) < want {
		header, remainder, ok := bytes.Cut(rest, []byte{'\n'})
		if !ok {
			return nil, fmt.Errorf("git cat-file --batch: response ended after %d of %d entries", len(entries), want)
		}
		rest = remainder

		// Absence is a structured line, not an exit status and not a message: it
		// is the one place git tells us "no such object" in a form that is the
		// same in every locale.
		if line := string(header); strings.HasSuffix(line, " missing") {
			entries = append(entries, batchEntry{missing: true})
			continue
		}

		fields := strings.Fields(string(header))
		if len(fields) != 3 {
			return nil, fmt.Errorf("git cat-file --batch: unreadable response header with %d fields", len(fields))
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil || size < 0 {
			return nil, fmt.Errorf("git cat-file --batch: unreadable object size %q", fields[2])
		}
		if len(rest) < size+1 {
			return nil, fmt.Errorf("git cat-file --batch: response holds %d bytes where the header promised %d", len(rest), size)
		}
		oid := objectID(fields[0])
		if !oid.valid() {
			return nil, fmt.Errorf("git cat-file --batch: %q is not an object id", fields[0])
		}

		entries = append(entries, batchEntry{
			oid: oid,
			typ: fields[1],
			// Copied, not aliased: markdown.Parse hands the caller slices of
			// whatever it was given, and core promises the caller may mutate
			// what a repository returns.
			data: bytes.Clone(rest[:size]),
		})
		rest = rest[size+1:]
	}
	return entries, nil
}

// readBlob reads the blob a spec names, and turns everything that is not a blob
// into an absence.
//
// A missing object and a tree are both [core.ErrNotFound]: a directory is not a
// document, and core has no more accurate word for "there is nothing of that
// kind here". what names the thing in the error, and is never the document's
// bytes.
func (r *Repository) readBlob(ctx context.Context, spec, what string) (objectID, []byte, error) {
	entries, err := r.catFile(ctx, spec)
	if err != nil {
		return "", nil, err
	}
	entry := entries[0]
	switch {
	case entry.missing:
		return "", nil, sentinelf(core.ErrNotFound, nil, "%s", what)
	case entry.typ == treeType:
		return "", nil, sentinelf(core.ErrNotFound, nil, "%s is a directory, not a document", what)
	case entry.typ != blobType:
		return "", nil, sentinelf(core.ErrNotFound, nil, "%s is a %s, not a document", what, entry.typ)
	}
	return entry.oid, entry.data, nil
}

// head resolves the branch to a commit and reports whether it has one.
//
// An unborn branch is not an error: git init is how a repository starts, so
// refusing one would make Micawber unable to bootstrap a repository it was
// pointed at. rev-parse --verify --quiet distinguishes the two cleanly -- exit 1
// with nothing on either stream -- without reading a message.
func (r *Repository) head(ctx context.Context) (objectID, bool, error) {
	out, err := r.exec.run(ctx, invocation{args: []string{"rev-parse", "--verify", "--quiet", r.ref + "^{commit}"}})
	if err != nil {
		var gitErr *gitError
		if errors.As(err, &gitErr) && gitErr.code == 1 && gitErr.stderr == "" {
			return "", false, nil
		}
		return "", false, fmt.Errorf("git: resolve %s: %w", r.ref, err)
	}

	oid := objectID(strings.TrimSpace(string(out)))
	if !oid.valid() {
		return "", false, fmt.Errorf("git: resolve %s: %q is not an object id", r.ref, oid)
	}
	return oid, true, nil
}

// treeRecord is one entry of a "git ls-tree -r -z" listing.
type treeRecord struct {
	mode string
	typ  string
	oid  objectID
	path string
}

// regularFileModes are the tree modes that mean "an ordinary file". A symbolic
// link is 120000 and a submodule is 160000; neither is a document, and treating
// a link as one would invite a Put that quietly turned it into a regular file.
var regularFileModes = []string{"100644", "100755"}

// parseLsTree splits a "-z" tree listing into records.
//
// Each record is "<mode> <type> <oid>\t<path>" and is NUL-terminated. The path is
// the only field that can hold a space and it comes after the tab, so the split
// is unambiguous: cut at the first tab, and the left half has exactly three
// fields. -z is also what stops git quoting unusual path names, which is what
// makes a path with a backslash in it arrive as the bytes git stored.
func parseLsTree(out []byte) ([]treeRecord, error) {
	var records []treeRecord

	for _, raw := range bytes.Split(out, []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		head, path, ok := bytes.Cut(raw, []byte{'\t'})
		if !ok {
			return nil, fmt.Errorf("git ls-tree: record with no tab separator")
		}
		fields := strings.Fields(string(head))
		if len(fields) != 3 {
			return nil, fmt.Errorf("git ls-tree: record with %d fields before the tab, want 3", len(fields))
		}
		oid := objectID(fields[2])
		if !oid.valid() {
			return nil, fmt.Errorf("git ls-tree: %q is not an object id", fields[2])
		}
		records = append(records, treeRecord{mode: fields[0], typ: fields[1], oid: oid, path: string(path)})
	}
	return records, nil
}

// isDocument reports whether a tree record could be a Markdown document at all:
// an ordinary file holding a blob.
func (rec treeRecord) isDocument() bool {
	return rec.typ == blobType && slices.Contains(regularFileModes, rec.mode)
}
