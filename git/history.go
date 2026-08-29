package git

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/shutx-net/micawber/core"
	"github.com/shutx-net/micawber/markdown"
)

// logFormat is the record git writes per commit: the object id, the author's
// name and e-mail, the author time, and the raw message, separated by NUL bytes.
//
// The message is last because it is the only field that can hold a newline, and
// -z terminates each record with a NUL of its own, so the whole stream is just
// fields with no framing left to guess at. %aI is strict ISO 8601, which
// time.Parse reads as RFC 3339; it is also what sets this package's git floor at
// 2.2.
const logFormat = "%H%x00%an%x00%ae%x00%aI%x00%B"

// logFields is how many fields logFormat writes per commit.
const logFields = 5

// History returns the revisions of p, most recent first.
//
// It is one path-filtered "git log" for the commits and one "git cat-file
// --batch" for the blob each of them left at that path -- two processes for any
// length of history, not two per entry. A commit that removed the document
// contributes no revision, because there is no content to have one.
//
// A limit above zero caps the result; zero or less means no limit. A path with no
// history at all is an error matching [core.ErrNotFound]. A path whose document
// has been deleted is not: its past revisions are exactly what a caller needs in
// order to restore it, so history outlives the document.
//
// --follow is deliberately not passed, so a document's history stops at a rename.
// Rename detection is a similarity heuristic, and a revision list that changed
// shape because Git's guess changed would be a poor thing to build a
// compare-and-swap on.
func (r *Repository) History(ctx context.Context, p core.ContentPath, limit int) ([]core.RevisionInfo, error) {
	repoRelative, err := r.repoPath(p)
	if err != nil {
		return nil, err
	}
	spec, err := pathspec(repoRelative)
	if err != nil {
		return nil, err
	}

	// --no-follow and --no-abbrev are both defences against configuration rather
	// than decoration: log.follow would silently turn rename detection on, and
	// --raw-style abbreviation would silently truncate an object id into
	// something that compares unequal to every revision Get ever returned.
	args := []string{"log", "--no-follow", "--no-abbrev", "--format=" + logFormat, "-z"}
	if limit > 0 {
		args = append(args, "-n", strconv.Itoa(limit))
	}
	args = append(args, r.ref, operandSeparator, spec)

	out, err := r.exec.run(ctx, invocation{args: args})
	if err != nil {
		// An unborn branch has no history rather than a broken one.
		if _, born, headErr := r.head(ctx); headErr == nil && !born {
			return nil, sentinelf(core.ErrNotFound, nil, "no document at %q", p)
		}
		return nil, fmt.Errorf("git: history of %q: %w", p, err)
	}

	commits, err := parseLog(out)
	if err != nil {
		return nil, fmt.Errorf("git: history of %q: %w", p, err)
	}
	if len(commits) == 0 {
		// git log exits 0 with no output for a path nothing was committed to,
		// so absence is an empty result rather than a failure.
		return nil, sentinelf(core.ErrNotFound, nil, "no document at %q", p)
	}

	specs := make([]string, 0, len(commits))
	for _, c := range commits {
		specs = append(specs, string(c.commit)+":"+repoRelative)
	}
	blobs, err := r.catFile(ctx, specs...)
	if err != nil {
		return nil, fmt.Errorf("git: history of %q: %w", p, err)
	}

	infos := make([]core.RevisionInfo, 0, len(commits))
	for i, c := range commits {
		if blobs[i].missing || blobs[i].typ != blobType {
			// The commit that removed the document. It changed the path, which
			// is why git listed it, but it left no content to have a revision.
			continue
		}
		infos = append(infos, core.RevisionInfo{
			Revision: core.Revision(blobs[i].oid),
			Author:   c.author,
			Time:     c.when,
			Message:  c.message,
		})
	}
	return infos, nil
}

// GetRevision returns p as it stood at rev.
//
// The revision must appear in p's own history, and that is checked before
// anything is read. Without the check this method would be an arbitrary-object
// read oracle wearing a path argument: "git cat-file blob" will return any object
// in the database, including one belonging to another document, one on another
// branch, one outside the content root, or one fetched from a remote and never
// checked out. Checking first also keeps those bytes from ever being in memory
// beside an error path.
//
// The check costs one unbounded history walk per call. Bounding it would make it
// cheap and would make deep history unreachable, which is the wrong trade for the
// method whose whole job is to reach into the past.
func (r *Repository) GetRevision(ctx context.Context, p core.ContentPath, rev core.Revision) (core.Content, error) {
	if rev.IsZero() {
		return core.Content{}, sentinelf(core.ErrNotFound, nil, "the zero revision names no version of %q", p)
	}

	infos, err := r.History(ctx, p, 0)
	if err != nil {
		return core.Content{}, err
	}
	found := false
	for _, info := range infos {
		if info.Revision == rev {
			found = true
			break
		}
	}
	if !found {
		// Deliberately says nothing about whether the object exists elsewhere.
		return core.Content{}, sentinelf(core.ErrNotFound, nil, "%q has no such revision", p)
	}

	// Read by object id. It reached here only by matching a revision this
	// package produced from git's own output, so it is an object name and not a
	// caller's string; it travels on standard input regardless.
	oid, data, err := r.readBlob(ctx, string(rev), fmt.Sprintf("revision %q of %q", rev, p))
	if err != nil {
		return core.Content{}, err
	}

	doc, err := markdown.Parse(data)
	if err != nil {
		return core.Content{}, fmt.Errorf("git: document %q at revision %q: %w", p, rev, err)
	}
	c := doc.Content
	c.Path = p
	c.Revision = core.Revision(oid)
	return c, nil
}

// logRecord is one commit as parseLog read it, before its blob is looked up.
type logRecord struct {
	commit  objectID
	author  core.Author
	when    time.Time
	message string
}

// parseLog splits a "-z" log into records.
//
// With -z and no diff output, git writes exactly logFields NUL-terminated fields
// per commit and nothing else, so the stream is a flat list of fields: a count
// that is not a multiple of logFields means the format and this parser have
// drifted apart, which is an error rather than something to recover from.
func parseLog(out []byte) ([]logRecord, error) {
	if len(out) == 0 {
		return nil, nil
	}

	fields := bytes.Split(out, []byte{0})
	// -z terminates the last record too, so the split leaves one empty tail.
	if len(fields) > 0 && len(fields[len(fields)-1]) == 0 {
		fields = fields[:len(fields)-1]
	}
	if len(fields)%logFields != 0 {
		return nil, fmt.Errorf("git log: %d fields, not a multiple of %d", len(fields), logFields)
	}

	records := make([]logRecord, 0, len(fields)/logFields)
	for i := 0; i < len(fields); i += logFields {
		commit := objectID(fields[i])
		if !commit.valid() {
			return nil, fmt.Errorf("git log: %q is not an object id", commit)
		}
		when, err := time.Parse(time.RFC3339, string(fields[i+3]))
		if err != nil {
			return nil, fmt.Errorf("git log: unreadable author time: %w", err)
		}
		records = append(records, logRecord{
			commit: commit,
			author: core.Author{Name: string(fields[i+1]), Email: string(fields[i+2])},
			when:   when,
			// Verbatim, as git stores it. commit-tree ends a message with a
			// newline whether or not the author wrote one, so trimming here
			// would be guessing at which of them put it there.
			message: string(fields[i+4]),
		})
	}
	return records, nil
}
