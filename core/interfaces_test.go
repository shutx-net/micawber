package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubRepo is a map-backed ContentRepository used to prove the interface is
// implementable and that the revision semantics in its doc comment can
// actually be expressed. It is a test fixture, not an adapter.
type stubRepo struct {
	mu   sync.Mutex
	docs map[ContentPath]Content
	seq  int
}

func newStubRepo() *stubRepo {
	return &stubRepo{docs: make(map[ContentPath]Content)}
}

func (r *stubRepo) List(ctx context.Context, c Collection) ([]ContentEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	entries := make([]ContentEntry, 0, len(r.docs))
	for path, doc := range r.docs {
		if !path.IsMarkdown() || !c.Contains(path) {
			continue
		}
		entries = append(entries, ContentEntry{Path: path, Revision: doc.Revision})
	}
	slices.SortFunc(entries, func(a, b ContentEntry) int {
		return strings.Compare(a.Path.String(), b.Path.String())
	})
	return entries, nil
}

func (r *stubRepo) Get(ctx context.Context, p ContentPath) (Content, error) {
	if err := ctx.Err(); err != nil {
		return Content{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	doc, ok := r.docs[p]
	if !ok {
		return Content{}, fmt.Errorf("get %q: %w", p, ErrNotFound)
	}
	return doc.Clone(), nil
}

func (r *stubRepo) Put(ctx context.Context, c Content, ch Change) (Revision, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := c.Validate(); err != nil {
		return "", err
	}
	if err := ch.Validate(); err != nil {
		return "", err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	stored, exists := r.docs[c.Path]
	switch {
	case c.Revision.IsZero() && exists:
		return "", fmt.Errorf("put %q: %w", c.Path, ErrExists)
	case !c.Revision.IsZero() && !exists:
		return "", fmt.Errorf("put %q: %w", c.Path, ErrNotFound)
	case !c.Revision.IsZero() && c.Revision != stored.Revision:
		return "", fmt.Errorf("put %q: %w", c.Path, ErrConflict)
	}

	r.seq++
	next := Revision(fmt.Sprintf("r%d", r.seq))
	doc := c.Clone()
	doc.Revision = next
	r.docs[c.Path] = doc
	return next, nil
}

func (r *stubRepo) Delete(ctx context.Context, p ContentPath, rev Revision, ch Change) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ch.Validate(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	stored, exists := r.docs[p]
	if !exists {
		return fmt.Errorf("delete %q: %w", p, ErrNotFound)
	}
	if !rev.IsZero() && rev != stored.Revision {
		return fmt.Errorf("delete %q: %w", p, ErrConflict)
	}
	delete(r.docs, p)
	return nil
}

// historyRepo is a stubRepo that also implements ContentHistory, standing in
// for a backend that keeps only the current version.
type historyRepo struct {
	*stubRepo
}

func (r *historyRepo) History(ctx context.Context, p ContentPath, limit int) ([]RevisionInfo, error) {
	doc, err := r.Get(ctx, p)
	if err != nil {
		return nil, err
	}
	if limit == 1 || limit <= 0 {
		return []RevisionInfo{{
			Revision: doc.Revision,
			Author:   Author{Name: "Ada", Email: "ada@example.com"},
			Time:     time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),
			Message:  "Add a post",
		}}, nil
	}
	return nil, nil
}

func (r *historyRepo) GetRevision(ctx context.Context, p ContentPath, rev Revision) (Content, error) {
	doc, err := r.Get(ctx, p)
	if err != nil {
		return Content{}, err
	}
	if doc.Revision != rev {
		return Content{}, fmt.Errorf("get revision %q of %q: %w", rev, p, ErrNotFound)
	}
	return doc, nil
}

// stubStore is a map-backed AssetStore. Like stubRepo it is a fixture, not an
// adapter: nothing here is meant to ship.
type stubStore struct {
	mu      sync.Mutex
	objects map[AssetKey]storedObject
}

type storedObject struct {
	ref  AssetRef
	data []byte
}

func newStubStore() *stubStore {
	return &stubStore{objects: make(map[AssetKey]storedObject)}
}

func (s *stubStore) Put(ctx context.Context, a Asset, r io.Reader) (AssetRef, error) {
	if err := ctx.Err(); err != nil {
		return AssetRef{}, err
	}
	if err := a.Validate(); err != nil {
		return AssetRef{}, err
	}

	data, err := io.ReadAll(r)
	if err != nil {
		return AssetRef{}, fmt.Errorf("read asset %q: %w", a.Key, err)
	}
	sum := sha256.Sum256(data)
	digest, err := NewDigest("sha256", hex.EncodeToString(sum[:]))
	if err != nil {
		return AssetRef{}, err
	}

	ref := AssetRef{
		Key:         a.Key,
		Size:        int64(len(data)),
		ContentType: a.ContentType,
		Digest:      digest,
		ModTime:     time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[a.Key] = storedObject{ref: ref, data: data}
	return ref, nil
}

func (s *stubStore) Get(ctx context.Context, key AssetKey) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	object, ok := s.objects[key]
	if !ok {
		return nil, fmt.Errorf("get asset %q: %w", key, ErrNotFound)
	}
	return io.NopCloser(bytes.NewReader(slices.Clone(object.data))), nil
}

func (s *stubStore) Stat(ctx context.Context, key AssetKey) (AssetRef, error) {
	if err := ctx.Err(); err != nil {
		return AssetRef{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	object, ok := s.objects[key]
	if !ok {
		return AssetRef{}, fmt.Errorf("stat asset %q: %w", key, ErrNotFound)
	}
	return object.ref, nil
}

func (s *stubStore) Delete(ctx context.Context, key AssetKey) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.objects[key]; !ok {
		return fmt.Errorf("delete asset %q: %w", key, ErrNotFound)
	}
	delete(s.objects, key)
	return nil
}

// testChange is the authorship the interface tests record.
func testChange() Change {
	return Change{
		Message: "Add a post",
		Author:  Author{Name: "Ada", Email: "ada@example.com"},
	}
}

func TestContentRepositoryIsImplementable(t *testing.T) {
	var repo ContentRepository = newStubRepo()

	ctx := context.Background()
	path := mustContentPath(t, "posts/hello.md")
	change := testChange()

	rev, err := repo.Put(ctx, Content{Path: path, Body: []byte("# Hello\n")}, change)
	if err != nil {
		t.Fatalf("Put error = %v; want nil", err)
	}
	if rev.IsZero() {
		t.Errorf("Put returned the zero Revision; want the revision of the stored content")
	}

	entries, err := repo.List(ctx, Collection{})
	if err != nil {
		t.Fatalf("List error = %v; want nil", err)
	}
	if len(entries) != 1 || entries[0].Path != path {
		t.Errorf("List = %v; want one entry for %q", entries, path)
	}

	got, err := repo.Get(ctx, path)
	if err != nil {
		t.Fatalf("Get error = %v; want nil", err)
	}
	if string(got.Body) != "# Hello\n" {
		t.Errorf("Get Body = %q; want %q", got.Body, "# Hello\n")
	}

	if err := repo.Delete(ctx, path, rev, change); err != nil {
		t.Fatalf("Delete error = %v; want nil", err)
	}
}

func TestAssetStoreIsImplementable(t *testing.T) {
	var store AssetStore = newStubStore()

	ctx := context.Background()
	key := mustAssetKey(t, "uploads/logo.png")
	data := []byte("not really a png")

	ref, err := store.Put(ctx, Asset{Key: key, ContentType: "image/png", Size: int64(len(data))}, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Put error = %v; want nil", err)
	}
	if err := ref.Validate(); err != nil {
		t.Errorf("returned AssetRef is invalid: %v", err)
	}
	if ref.Size != int64(len(data)) {
		t.Errorf("Put ref Size = %d; want %d", ref.Size, len(data))
	}

	rc, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get error = %v; want nil", err)
	}
	read, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll error = %v; want nil", err)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("Close error = %v; want nil", err)
	}
	if !bytes.Equal(read, data) {
		t.Errorf("Get = %q; want %q", read, data)
	}

	if _, err := store.Stat(ctx, key); err != nil {
		t.Fatalf("Stat error = %v; want nil", err)
	}
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("Delete error = %v; want nil", err)
	}
	if err := store.Delete(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete of a missing asset = %v; want errors.Is(err, ErrNotFound)", err)
	}
}

func TestContentHistoryIsOptional(t *testing.T) {
	var plain ContentRepository = newStubRepo()
	if _, ok := plain.(ContentHistory); ok {
		t.Errorf("*stubRepo satisfies ContentHistory; want a repository without history not to")
	}

	var withHistory ContentRepository = &historyRepo{stubRepo: newStubRepo()}
	capable, ok := withHistory.(ContentHistory)
	if !ok {
		t.Fatalf("*historyRepo does not satisfy ContentHistory; want the capability detectable by type assertion")
	}

	ctx := context.Background()
	path := mustContentPath(t, "posts/hello.md")
	rev, err := withHistory.Put(ctx, Content{Path: path, Body: []byte("# Hello\n")}, testChange())
	if err != nil {
		t.Fatalf("Put error = %v; want nil", err)
	}

	infos, err := capable.History(ctx, path, 0)
	if err != nil {
		t.Fatalf("History error = %v; want nil", err)
	}
	if len(infos) != 1 || infos[0].Revision != rev {
		t.Errorf("History = %v; want one entry at revision %q", infos, rev)
	}

	got, err := capable.GetRevision(ctx, path, rev)
	if err != nil {
		t.Fatalf("GetRevision error = %v; want nil", err)
	}
	if got.Revision != rev {
		t.Errorf("GetRevision Revision = %q; want %q", got.Revision, rev)
	}
	if _, err := capable.GetRevision(ctx, path, Revision("nope")); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetRevision of an unknown revision = %v; want errors.Is(err, ErrNotFound)", err)
	}
}

func TestStubRepositoryHonoursRevisionSemantics(t *testing.T) {
	var repo ContentRepository = newStubRepo()

	ctx := context.Background()
	path := mustContentPath(t, "posts/hello.md")
	change := testChange()
	doc := Content{Path: path, Body: []byte("# Hello\n")}

	if _, err := repo.Get(ctx, path); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get of a missing path = %v; want errors.Is(err, ErrNotFound)", err)
	}

	first, err := repo.Put(ctx, doc, change)
	if err != nil {
		t.Fatalf("create error = %v; want nil", err)
	}
	if first.IsZero() {
		t.Fatalf("create returned the zero Revision; want a revision")
	}

	if _, err := repo.Put(ctx, doc, change); !errors.Is(err, ErrExists) {
		t.Errorf("create over an existing path = %v; want errors.Is(err, ErrExists)", err)
	}

	updated := doc.Clone()
	updated.Revision = first
	second, err := repo.Put(ctx, updated, change)
	if err != nil {
		t.Fatalf("update at the current revision error = %v; want nil", err)
	}
	if second == first {
		t.Errorf("update returned revision %q; want one different from %q", second, first)
	}

	stale := doc.Clone()
	stale.Revision = first
	if _, err := repo.Put(ctx, stale, change); !errors.Is(err, ErrConflict) {
		t.Errorf("update at a stale revision = %v; want errors.Is(err, ErrConflict)", err)
	}

	missing := Content{Path: mustContentPath(t, "posts/absent.md"), Revision: first, Body: []byte("x")}
	if _, err := repo.Put(ctx, missing, change); !errors.Is(err, ErrNotFound) {
		t.Errorf("update of a missing path = %v; want errors.Is(err, ErrNotFound)", err)
	}

	if err := repo.Delete(ctx, path, first, change); !errors.Is(err, ErrConflict) {
		t.Errorf("delete at a stale revision = %v; want errors.Is(err, ErrConflict)", err)
	}
	if err := repo.Delete(ctx, path, second, change); err != nil {
		t.Fatalf("delete at the current revision error = %v; want nil", err)
	}
	if err := repo.Delete(ctx, path, "", change); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete of a missing path = %v; want errors.Is(err, ErrNotFound)", err)
	}
}

func TestStubRepositoryListIsBoundedByCollection(t *testing.T) {
	var repo ContentRepository = newStubRepo()

	ctx := context.Background()
	change := testChange()
	for _, p := range []string{"posts/b.md", "posts/a.md", "posts/2026/c.md", "posts/data.json", "pages/x.md"} {
		if _, err := repo.Put(ctx, Content{Path: mustContentPath(t, p), Body: []byte("x")}, change); err != nil {
			t.Fatalf("Put(%q) error = %v; want nil", p, err)
		}
	}

	paths := func(entries []ContentEntry) []string {
		out := make([]string, 0, len(entries))
		for _, e := range entries {
			out = append(out, e.Path.String())
		}
		return out
	}

	posts, err := repo.List(ctx, mustCollection(t, "posts"))
	if err != nil {
		t.Fatalf("List error = %v; want nil", err)
	}
	want := []string{"posts/2026/c.md", "posts/a.md", "posts/b.md"}
	if got := paths(posts); !slices.Equal(got, want) {
		t.Errorf("List(posts) = %v; want %v: recursive, Markdown only, sorted by path", got, want)
	}
	for _, e := range posts {
		if e.Revision.IsZero() {
			t.Errorf("entry %q has the zero Revision; want the stored revision", e.Path)
		}
	}

	all, err := repo.List(ctx, Collection{})
	if err != nil {
		t.Fatalf("List(root) error = %v; want nil", err)
	}
	wantAll := []string{"pages/x.md", "posts/2026/c.md", "posts/a.md", "posts/b.md"}
	if got := paths(all); !slices.Equal(got, wantAll) {
		t.Errorf("List(root) = %v; want %v", got, wantAll)
	}

	empty, err := repo.List(ctx, mustCollection(t, "unknown"))
	if err != nil {
		t.Fatalf("List of an unknown collection error = %v; want nil: a collection is a prefix, not an object", err)
	}
	if len(empty) != 0 {
		t.Errorf("List of an unknown collection = %v; want no entries", empty)
	}
}
