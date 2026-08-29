package git

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shutx-net/micawber/core"
)

// The size of the stochastic tests below. Eight writers is enough for the
// interleaving to actually happen and small enough that the suite stays quick
// under -race, where every Put is six subprocesses.
const (
	concurrentWriters  = 8
	concurrentAttempts = 20
)

// outcome counts what a set of racing writers got back. It exists so that the
// assertions are about an invariant that must hold whatever the interleaving
// was, rather than about a particular one.
type outcome struct {
	mu        sync.Mutex
	succeeded int
	conflicts int
	exists    int
	notFound  int
	others    []error
}

func (o *outcome) record(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	switch {
	case err == nil:
		o.succeeded++
	case errors.Is(err, core.ErrConflict):
		o.conflicts++
	case errors.Is(err, core.ErrExists):
		o.exists++
	case errors.Is(err, core.ErrNotFound):
		o.notFound++
	default:
		o.others = append(o.others, err)
	}
}

// total is every call that was made.
func (o *outcome) total() int {
	return o.succeeded + o.conflicts + o.exists + o.notFound + len(o.others)
}

// TestConcurrentWritersToOneDocumentLoseNoUpdates asserts the invariant no
// single interleaving could establish: whatever order the writers ran in, every
// call either wrote or was told the document had changed, and the number of
// commits equals the number that wrote.
//
// A lost update -- two writers both told they succeeded, one of their commits
// missing -- would show up as a commit count below the success count, and
// nothing else in this package would catch it.
func TestConcurrentWritersToOneDocumentLoseNoUpdates(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"posts/a.md": []byte("---\ntitle: a\n---\n\nzero\n")})
	opened := repo.open()
	before := repo.commitCount()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()

	var got outcome
	var wg sync.WaitGroup
	for writer := range concurrentWriters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for attempt := range concurrentAttempts {
				c, err := opened.Get(ctx, mustPathNoFatal("posts/a.md"))
				if err != nil {
					got.record(fmt.Errorf("Get: %w", err))
					continue
				}
				c.Body = []byte(fmt.Sprintf("\nwriter %d attempt %d\n", writer, attempt))
				_, err = opened.Put(ctx, c, testChange(fmt.Sprintf("writer %d attempt %d", writer, attempt)))
				got.record(err)
			}
		}()
	}
	wg.Wait()

	t.Logf("%d writers x %d attempts: %d succeeded, %d conflicted, %d other",
		concurrentWriters, concurrentAttempts, got.succeeded, got.conflicts, len(got.others))

	if len(got.others) > 0 {
		t.Fatalf("unexpected errors, first of %d: %v", len(got.others), got.others[0])
	}
	if want := concurrentWriters * concurrentAttempts; got.total() != want {
		t.Fatalf("counted %d outcomes, want %d", got.total(), want)
	}
	if got.succeeded+got.conflicts != concurrentWriters*concurrentAttempts {
		t.Errorf("%d succeeded and %d conflicted; every call must be one or the other", got.succeeded, got.conflicts)
	}
	if got.succeeded == 0 {
		t.Fatal("no writer succeeded; the test would pass vacuously")
	}
	if got.conflicts == 0 {
		t.Error("no writer conflicted; the writers did not actually overlap, so this run proves nothing")
	}
	if after, want := repo.commitCount(), before+got.succeeded; after != want {
		t.Errorf("commit count = %d, want %d (%d before plus one per success); an update was lost", after, want, before)
	}
	repo.fsck()

	final, err := opened.Get(t.Context(), mustPath(t, "posts/a.md"))
	if err != nil {
		t.Fatalf("Get after the race: %v", err)
	}
	if !strings.HasPrefix(string(final.Body), "\nwriter ") {
		t.Errorf("the final body is %q, which nobody wrote", final.Body)
	}
}

// TestConcurrentWritersToDifferentDocumentsAllSucceed is the liveness half, and
// the test that justifies the retry loop existing at all.
//
// Every writer owns its own document, so no writer's document is ever changed by
// another, so no call may report a conflict. A naive implementation that mapped a
// failed ref compare-and-swap straight to ErrConflict fails here, and that is the
// most likely way to get this wrong -- it looks correct, and it turns a
// per-document guarantee back into a per-repository one.
func TestConcurrentWritersToDifferentDocumentsAllSucceed(t *testing.T) {
	repo := newTestRepo(t)
	seed := map[string][]byte{}
	for writer := range concurrentWriters {
		seed[fmt.Sprintf("posts/w%d.md", writer)] = []byte("---\ntitle: w\n---\n\nzero\n")
	}
	repo.commit("first", seed)
	opened := repo.open()
	before := repo.commitCount()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()

	var got outcome
	var wg sync.WaitGroup
	for writer := range concurrentWriters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path := mustPathNoFatal(fmt.Sprintf("posts/w%d.md", writer))
			for attempt := range concurrentAttempts {
				c, err := opened.Get(ctx, path)
				if err != nil {
					got.record(fmt.Errorf("Get: %w", err))
					continue
				}
				c.Body = []byte(fmt.Sprintf("\nattempt %d\n", attempt))
				_, err = opened.Put(ctx, c, testChange(fmt.Sprintf("writer %d attempt %d", writer, attempt)))
				got.record(err)
			}
		}()
	}
	wg.Wait()

	t.Logf("%d writers x %d attempts on distinct documents: %d succeeded, %d conflicted, %d other",
		concurrentWriters, concurrentAttempts, got.succeeded, got.conflicts, len(got.others))

	if len(got.others) > 0 {
		t.Fatalf("unexpected errors, first of %d: %v", len(got.others), got.others[0])
	}
	if got.conflicts != 0 {
		t.Errorf("%d calls conflicted; writers on different documents must never collide", got.conflicts)
	}
	if want := concurrentWriters * concurrentAttempts; got.succeeded != want {
		t.Errorf("%d of %d calls succeeded", got.succeeded, want)
	}
	if after, want := repo.commitCount(), before+got.succeeded; after != want {
		t.Errorf("commit count = %d, want %d; a commit was lost", after, want)
	}
	repo.fsck()
}

// TestConcurrentCreatesOfTheSamePathYieldExactlyOneWinner is the create half of
// the compare-and-swap under a race.
func TestConcurrentCreatesOfTheSamePathYieldExactlyOneWinner(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"posts/seed.md": []byte("---\ntitle: s\n---\n\ns\n")})
	opened := repo.open()

	var got outcome
	var wg sync.WaitGroup
	for writer := range concurrentWriters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := core.Content{
				Path:        mustPathNoFatal("posts/contested.md"),
				FrontMatter: core.FrontMatter{Format: core.FrontMatterYAML, Fields: map[string]any{"title": "contested"}},
				Body:        []byte(fmt.Sprintf("\nwriter %d\n", writer)),
			}
			_, err := opened.Put(t.Context(), c, testChange(fmt.Sprintf("writer %d creates it", writer)))
			got.record(err)
		}()
	}
	wg.Wait()

	if len(got.others) > 0 {
		t.Fatalf("unexpected errors, first of %d: %v", len(got.others), got.others[0])
	}
	if got.succeeded != 1 {
		t.Errorf("%d writers created the document, want exactly 1", got.succeeded)
	}
	if got.exists != concurrentWriters-1 {
		t.Errorf("%d writers were told it already exists, want %d", got.exists, concurrentWriters-1)
	}
	repo.fsck()
}

// TestConcurrentFirstCommitsOnAnUnbornBranchYieldExactlyOneWinner covers the
// create-only compare-and-swap, where the old value is the empty string.
//
// It is worth its own test because it is the one publish that has no parent to
// swap on, and because a writer that treated "the branch does not exist yet" as
// "nothing can conflict" would silently overwrite the winner.
func TestConcurrentFirstCommitsOnAnUnbornBranchYieldExactlyOneWinner(t *testing.T) {
	repo := newTestRepo(t)
	opened := repo.open()

	var got outcome
	var wg sync.WaitGroup
	for writer := range concurrentWriters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := core.Content{
				Path:        mustPathNoFatal("posts/first.md"),
				FrontMatter: core.FrontMatter{Format: core.FrontMatterYAML, Fields: map[string]any{"title": "first"}},
				Body:        []byte(fmt.Sprintf("\nwriter %d\n", writer)),
			}
			_, err := opened.Put(t.Context(), c, testChange(fmt.Sprintf("writer %d makes the first commit", writer)))
			got.record(err)
		}()
	}
	wg.Wait()

	if len(got.others) > 0 {
		t.Fatalf("unexpected errors, first of %d: %v", len(got.others), got.others[0])
	}
	if got.succeeded != 1 {
		t.Errorf("%d writers made the first commit, want exactly 1", got.succeeded)
	}
	if got.exists != concurrentWriters-1 {
		t.Errorf("%d writers were told it already exists, want %d", got.exists, concurrentWriters-1)
	}
	if n := repo.commitCount(); n != 1 {
		t.Errorf("commit count = %d, want 1", n)
	}
	repo.fsck()
}

// TestConcurrentDeleteAndPutOfTheSameDocument is the mixed race: whoever loses
// must be told something true, and the repository must not end up in a state
// neither of them asked for.
func TestConcurrentDeleteAndPutOfTheSameDocument(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"posts/a.md": []byte("---\ntitle: a\n---\n\nzero\n")})
	opened := repo.open()
	p := mustPath(t, "posts/a.md")

	read, err := opened.Get(t.Context(), p)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	var putErr, deleteErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		edited := read
		edited.Body = []byte("\nedited\n")
		_, putErr = opened.Put(t.Context(), edited, testChange("edit it"))
	}()
	go func() {
		defer wg.Done()
		deleteErr = opened.Delete(t.Context(), p, read.Revision, testChange("remove it"))
	}()
	wg.Wait()

	t.Logf("put: %v; delete: %v", putErr, deleteErr)

	for _, err := range []error{putErr, deleteErr} {
		if err != nil && !errors.Is(err, core.ErrConflict) && !errors.Is(err, core.ErrNotFound) {
			t.Errorf("got %v, want nil, core.ErrConflict or core.ErrNotFound", err)
		}
	}
	if putErr == nil && deleteErr == nil {
		// Both were conditional on the same revision, so both cannot have
		// applied: one must have seen the other.
		t.Error("both the edit and the removal reported success against the same revision")
	}
	repo.fsck()
}

// TestPutAgainstARefMovedByPlainGitBetweenReadAndPublish is the deterministic
// form of the same property, with the interleaving injected instead of raced.
//
// It is the test that will actually say what broke when the stochastic ones go
// red: the branch moves under the write at the exact moment before it publishes,
// and the write must rebuild on the new tip rather than fail or clobber it.
func TestPutAgainstARefMovedByPlainGitBetweenReadAndPublish(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{"posts/a.md": []byte("---\ntitle: a\n---\n\nzero\n")})
	opened := repo.open()

	c, err := opened.Get(t.Context(), mustPath(t, "posts/a.md"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	moved := false
	opened.exec.observe = func(args []string) {
		if args[0] != "update-ref" || moved {
			return
		}
		moved = true
		repo.commit("plain git, mid-write", map[string][]byte{"posts/elsewhere.md": []byte("---\ntitle: e\n---\n\ne\n")})
	}

	c.Body = []byte("\nedited\n")
	if _, err := opened.Put(t.Context(), c, testChange("edit a")); err != nil {
		t.Fatalf("Put across a branch that moved just before publishing: %v", err)
	}
	if !moved {
		t.Fatal("the interleaving was never injected, so this run proves nothing")
	}

	if got, want := string(repo.blob("posts/a.md")), "---\ntitle: a\n---\n\nedited\n"; got != want {
		t.Errorf("stored bytes = %q, want %q", got, want)
	}
	if repo.blobAt("posts/elsewhere.md") == "" {
		t.Error("the commit made mid-write is gone; the retry rebuilt from a stale parent")
	}
	repo.fsck()
}

// TestRepositoryIsSafeForConcurrentUse exercises every operation at once against
// one *Repository, which is what the -race detector needs in order to say
// anything.
//
// The safety comes from the type holding nothing mutable after Open rather than
// from a lock: a mutex would serialise this process while doing nothing about the
// other processes and machines that can write the same repository, which is the
// appearance of safety exactly where the risk is.
func TestRepositoryIsSafeForConcurrentUse(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit("first", map[string][]byte{
		"posts/a.md": []byte("---\ntitle: a\n---\n\na\n"),
		"posts/b.md": []byte("---\ntitle: b\n---\n\nb\n"),
	})
	opened := repo.open()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	var wg sync.WaitGroup
	for i := range concurrentWriters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := mustPathNoFatal("posts/a.md")
			switch i % 4 {
			case 0:
				_, _ = opened.List(ctx, core.Collection{})
			case 1:
				_, _ = opened.Get(ctx, p)
			case 2:
				_, _ = opened.History(ctx, p, 0)
			case 3:
				c := core.Content{
					Path:        mustPathNoFatal(fmt.Sprintf("posts/new%d.md", i)),
					FrontMatter: core.FrontMatter{Format: core.FrontMatterYAML, Fields: map[string]any{"title": "n"}},
					Body:        []byte("\nn\n"),
				}
				_, _ = opened.Put(ctx, c, testChange("concurrent create"))
			}
		}()
	}
	wg.Wait()
	repo.fsck()
}

// mustPathNoFatal is mustPath for a goroutine, where testing.T.Fatalf may not be
// called. The inputs are all literals this package wrote, so a failure is a
// programming error rather than a test condition.
func mustPathNoFatal(s string) core.ContentPath {
	p, err := core.NewContentPath(s)
	if err != nil {
		panic("git: test path " + s + ": " + err.Error())
	}
	return p
}
