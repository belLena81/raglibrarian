package repository_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/pkg/domain"
	"github.com/belLena81/raglibrarian/services/metadata/repository"
)

// ── fakeBookRepo ──────────────────────────────────────────────────────────────
// In-memory implementation used across all unit tests.
// It enforces the same contracts as the Postgres implementation so the use
// case layer can be tested without a running database.

type fakeBookRepo struct {
	mu      sync.RWMutex
	books   map[string]domain.Book // keyed by id
	saveErr error
}

func newFakeBookRepo() *fakeBookRepo {
	return &fakeBookRepo{books: make(map[string]domain.Book)}
}

func (f *fakeBookRepo) Save(_ context.Context, b domain.Book) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveErr != nil {
		return f.saveErr
	}
	for _, existing := range f.books {
		if existing.Title() == b.Title() &&
			existing.Author() == b.Author() &&
			existing.Year() == b.Year() {
			return domain.ErrDuplicateBook
		}
	}
	f.books[b.Id()] = b
	return nil
}

func (f *fakeBookRepo) FindByID(_ context.Context, id string) (domain.Book, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	b, ok := f.books[id]
	if !ok {
		return domain.Book{}, domain.ErrBookNotFound
	}
	return b, nil
}

func (f *fakeBookRepo) List(_ context.Context, filter repository.ListFilter) ([]domain.Book, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	result := make([]domain.Book, 0, len(f.books))
	for _, b := range f.books {
		if !matchesFilter(b, filter) {
			continue
		}
		result = append(result, b)
	}
	return result, nil
}

func (f *fakeBookRepo) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.books[id]; !ok {
		return domain.ErrBookNotFound
	}
	delete(f.books, id)
	return nil
}

func (f *fakeBookRepo) UpdateStatus(_ context.Context, id string, next domain.Status) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.books[id]
	if !ok {
		return domain.ErrBookNotFound
	}
	if err := b.SetStatus(next); err != nil {
		return err
	}
	f.books[id] = b
	return nil
}

func (f *fakeBookRepo) UpdateS3Key(_ context.Context, id, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.books[id]
	if !ok {
		return domain.ErrBookNotFound
	}
	if err := b.SetS3Key(key); err != nil {
		return err
	}
	f.books[id] = b
	return nil
}

func matchesFilter(b domain.Book, f repository.ListFilter) bool {
	if f.Author != nil && b.Author() != *f.Author {
		return false
	}
	if f.YearFrom != nil && b.Year() < *f.YearFrom {
		return false
	}
	if f.YearTo != nil && b.Year() > *f.YearTo {
		return false
	}
	if f.Status != nil && b.Status() != *f.Status {
		return false
	}
	for _, tag := range f.Tags {
		if !containsTag(b.Tags(), tag) {
			return false
		}
	}
	return true
}

func containsTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

// Compile-time check: fake satisfies the interface.
var _ repository.BookRepository = (*fakeBookRepo)(nil)

// ── helpers ───────────────────────────────────────────────────────────────────

func newBook(t *testing.T, title, author string, year int) domain.Book {
	t.Helper()
	b, err := domain.NewBook(title, author, year)
	require.NoError(t, err)
	return b
}

func ptr[T any](v T) *T { return &v }

// ── Save ──────────────────────────────────────────────────────────────────────

func TestBookRepo_Save_Succeeds(t *testing.T) {
	repo := newFakeBookRepo()
	b := newBook(t, "Clean Code", "Robert Martin", 2008)

	require.NoError(t, repo.Save(context.Background(), b))

	got, err := repo.FindByID(context.Background(), b.Id())
	require.NoError(t, err)
	assert.Equal(t, b.Id(), got.Id())
	assert.Equal(t, "Clean Code", got.Title())
}

func TestBookRepo_Save_DuplicateTitleAuthorYear_ReturnsDuplicateBook(t *testing.T) {
	repo := newFakeBookRepo()
	b1 := newBook(t, "Clean Code", "Robert Martin", 2008)
	b2 := newBook(t, "Clean Code", "Robert Martin", 2008)
	require.NoError(t, repo.Save(context.Background(), b1))

	err := repo.Save(context.Background(), b2)

	assert.ErrorIs(t, err, domain.ErrDuplicateBook)
}

func TestBookRepo_Save_SameTitleDifferentYear_Succeeds(t *testing.T) {
	// Different edition — not a duplicate.
	repo := newFakeBookRepo()
	b1 := newBook(t, "The Pragmatic Programmer", "Hunt & Thomas", 1999)
	b2 := newBook(t, "The Pragmatic Programmer", "Hunt & Thomas", 2019)
	require.NoError(t, repo.Save(context.Background(), b1))

	assert.NoError(t, repo.Save(context.Background(), b2))
}

func TestBookRepo_Save_SameTitleDifferentAuthor_Succeeds(t *testing.T) {
	repo := newFakeBookRepo()
	b1 := newBook(t, "Introduction to Algorithms", "CLRS", 2009)
	b2 := newBook(t, "Introduction to Algorithms", "Other Author", 2009)
	require.NoError(t, repo.Save(context.Background(), b1))

	assert.NoError(t, repo.Save(context.Background(), b2))
}

// ── FindByID ──────────────────────────────────────────────────────────────────

func TestBookRepo_FindByID_Exists(t *testing.T) {
	repo := newFakeBookRepo()
	b := newBook(t, "DDIA", "Kleppmann", 2017)
	require.NoError(t, repo.Save(context.Background(), b))

	got, err := repo.FindByID(context.Background(), b.Id())

	require.NoError(t, err)
	assert.Equal(t, b.Id(), got.Id())
}

func TestBookRepo_FindByID_Missing_ReturnsBookNotFound(t *testing.T) {
	repo := newFakeBookRepo()

	_, err := repo.FindByID(context.Background(), "non-existent-id")

	assert.ErrorIs(t, err, domain.ErrBookNotFound)
}

// ── Delete ────────────────────────────────────────────────────────────────────

func TestBookRepo_Delete_Succeeds(t *testing.T) {
	repo := newFakeBookRepo()
	b := newBook(t, "Title", "Author", 2020)
	require.NoError(t, repo.Save(context.Background(), b))

	require.NoError(t, repo.Delete(context.Background(), b.Id()))

	_, err := repo.FindByID(context.Background(), b.Id())
	assert.ErrorIs(t, err, domain.ErrBookNotFound)
}

func TestBookRepo_Delete_Missing_ReturnsBookNotFound(t *testing.T) {
	repo := newFakeBookRepo()

	err := repo.Delete(context.Background(), "ghost-id")

	assert.ErrorIs(t, err, domain.ErrBookNotFound)
}

// ── UpdateStatus ─────────────────────────────────────────────────────────

func TestBookRepo_UpdateStatus_ValidTransition(t *testing.T) {
	repo := newFakeBookRepo()
	b := newBook(t, "Title", "Author", 2020)
	require.NoError(t, repo.Save(context.Background(), b))

	require.NoError(t, repo.UpdateStatus(context.Background(), b.Id(), domain.Indexing))

	got, err := repo.FindByID(context.Background(), b.Id())
	require.NoError(t, err)
	assert.Equal(t, domain.Indexing, got.Status())
}

func TestBookRepo_UpdateStatus_InvalidTransition_ReturnsDomainError(t *testing.T) {
	repo := newFakeBookRepo()
	b := newBook(t, "Title", "Author", 2020) // starts at pending
	require.NoError(t, repo.Save(context.Background(), b))

	// pending → indexed is forbidden.
	err := repo.UpdateStatus(context.Background(), b.Id(), domain.Indexed)

	assert.ErrorIs(t, err, domain.ErrInvalidStatusTransition)
}

func TestBookRepo_UpdateStatus_Missing_ReturnsBookNotFound(t *testing.T) {
	repo := newFakeBookRepo()

	err := repo.UpdateStatus(context.Background(), "ghost-id", domain.Indexing)

	assert.ErrorIs(t, err, domain.ErrBookNotFound)
}

func TestBookRepo_UpdateStatus_FullPipeline_Succeeds(t *testing.T) {
	// pending → indexing → indexed
	repo := newFakeBookRepo()
	b := newBook(t, "Title", "Author", 2020)
	require.NoError(t, repo.Save(context.Background(), b))
	ctx := context.Background()

	require.NoError(t, repo.UpdateStatus(ctx, b.Id(), domain.Indexing))
	require.NoError(t, repo.UpdateStatus(ctx, b.Id(), domain.Indexed))

	got, err := repo.FindByID(ctx, b.Id())
	require.NoError(t, err)
	assert.Equal(t, domain.Indexed, got.Status())
}

func TestBookRepo_UpdateStatus_ReindexFromFailed(t *testing.T) {
	// failed → pending is the reindex path.
	repo := newFakeBookRepo()
	b := newBook(t, "Title", "Author", 2020)
	require.NoError(t, repo.Save(context.Background(), b))
	ctx := context.Background()

	require.NoError(t, repo.UpdateStatus(ctx, b.Id(), domain.Indexing))
	require.NoError(t, repo.UpdateStatus(ctx, b.Id(), domain.Failed))
	require.NoError(t, repo.UpdateStatus(ctx, b.Id(), domain.Pending))

	got, _ := repo.FindByID(ctx, b.Id())
	assert.Equal(t, domain.Pending, got.Status())
}

// ── UpdateS3Key ───────────────────────────────────────────────────────────────

func TestBookRepo_UpdateS3Key_Succeeds(t *testing.T) {
	repo := newFakeBookRepo()
	b := newBook(t, "Title", "Author", 2020)
	require.NoError(t, repo.Save(context.Background(), b))

	require.NoError(t, repo.UpdateS3Key(context.Background(), b.Id(), "books/abc/file.pdf"))

	got, err := repo.FindByID(context.Background(), b.Id())
	require.NoError(t, err)
	assert.Equal(t, "books/abc/file.pdf", got.S3Key())
}

func TestBookRepo_UpdateS3Key_EmptyKey_ReturnsDomainError(t *testing.T) {
	repo := newFakeBookRepo()
	b := newBook(t, "Title", "Author", 2020)
	require.NoError(t, repo.Save(context.Background(), b))

	err := repo.UpdateS3Key(context.Background(), b.Id(), "")
	assert.ErrorIs(t, err, domain.ErrEmptyS3Key)
}

func TestBookRepo_UpdateS3Key_Missing_ReturnsBookNotFound(t *testing.T) {
	repo := newFakeBookRepo()

	err := repo.UpdateS3Key(context.Background(), "ghost-id", "books/x/file.pdf")

	assert.ErrorIs(t, err, domain.ErrBookNotFound)
}

// ── List ──────────────────────────────────────────────────────────────────────

func TestBookRepo_List_Empty_ReturnsEmptySlice(t *testing.T) {
	repo := newFakeBookRepo()

	books, err := repo.List(context.Background(), repository.ListFilter{})

	require.NoError(t, err)
	assert.NotNil(t, books)
	assert.Empty(t, books)
}

func TestBookRepo_List_NoFilter_ReturnsAll(t *testing.T) {
	repo := newFakeBookRepo()
	require.NoError(t, repo.Save(context.Background(), newBook(t, "Book A", "Author A", 2020)))
	require.NoError(t, repo.Save(context.Background(), newBook(t, "Book B", "Author B", 2021)))

	books, err := repo.List(context.Background(), repository.ListFilter{})

	require.NoError(t, err)
	assert.Len(t, books, 2)
}

func TestBookRepo_List_FilterByAuthor(t *testing.T) {
	repo := newFakeBookRepo()
	require.NoError(t, repo.Save(context.Background(), newBook(t, "Book A", "Martin", 2020)))
	require.NoError(t, repo.Save(context.Background(), newBook(t, "Book B", "Fowler", 2021)))

	books, err := repo.List(context.Background(), repository.ListFilter{Author: ptr("Martin")})

	require.NoError(t, err)
	require.Len(t, books, 1)
	assert.Equal(t, "Martin", books[0].Author())
}

func TestBookRepo_List_FilterByYearRange(t *testing.T) {
	repo := newFakeBookRepo()
	require.NoError(t, repo.Save(context.Background(), newBook(t, "Old", "A", 2000)))
	require.NoError(t, repo.Save(context.Background(), newBook(t, "Mid", "B", 2010)))
	require.NoError(t, repo.Save(context.Background(), newBook(t, "New", "C", 2020)))

	books, err := repo.List(context.Background(), repository.ListFilter{
		YearFrom: ptr(2005),
		YearTo:   ptr(2015),
	})

	require.NoError(t, err)
	require.Len(t, books, 1)
	assert.Equal(t, 2010, books[0].Year())
}

func TestBookRepo_List_FilterByStatus(t *testing.T) {
	repo := newFakeBookRepo()
	b1 := newBook(t, "Book A", "Author", 2020)
	b2 := newBook(t, "Book B", "Author", 2021)
	require.NoError(t, repo.Save(context.Background(), b1))
	require.NoError(t, repo.Save(context.Background(), b2))
	require.NoError(t, repo.UpdateStatus(context.Background(), b1.Id(), domain.Indexing))

	books, err := repo.List(context.Background(), repository.ListFilter{Status: new(domain.Pending)})

	require.NoError(t, err)
	require.Len(t, books, 1)
	assert.Equal(t, b2.Id(), books[0].Id())
}

func TestBookRepo_List_FilterByTags_AllTagsMustMatch(t *testing.T) {
	repo := newFakeBookRepo()
	ctx := context.Background()

	bGo := newBook(t, "Go Book", "Author", 2020)
	require.NoError(t, repo.Save(ctx, bGo))
	require.NoError(t, repo.UpdateS3Key(ctx, bGo.Id(), "k"))

	// re-fetch to set tags via the domain method
	bGo, _ = repo.FindByID(ctx, bGo.Id())
	_ = bGo.SetTags([]string{"go", "concurrency"})
	// re-save with tags (fake allows overwrite via direct put)
	repo.books[bGo.Id()] = bGo

	bRust := newBook(t, "Rust Book", "Author", 2021)
	require.NoError(t, repo.Save(ctx, bRust))
	bRust, _ = repo.FindByID(ctx, bRust.Id())
	_ = bRust.SetTags([]string{"rust"})
	repo.books[bRust.Id()] = bRust

	// Filter requires both tags — only bGo qualifies.
	books, err := repo.List(ctx, repository.ListFilter{Tags: []string{"go", "concurrency"}})
	require.NoError(t, err)
	require.Len(t, books, 1)
	assert.Equal(t, bGo.Id(), books[0].Id())
}

func TestBookRepo_List_FilterByTags_NoMatch_ReturnsEmpty(t *testing.T) {
	repo := newFakeBookRepo()
	b := newBook(t, "Go Book", "Author", 2020)
	require.NoError(t, repo.Save(context.Background(), b))

	books, err := repo.List(context.Background(), repository.ListFilter{Tags: []string{"haskell"}})

	require.NoError(t, err)
	assert.Empty(t, books)
}

func TestBookRepo_List_MultipleFilters_ANDSemantics(t *testing.T) {
	repo := newFakeBookRepo()
	ctx := context.Background()

	match := newBook(t, "Match", "Martin", 2020)
	noMatch1 := newBook(t, "Wrong Author", "Fowler", 2020)
	noMatch2 := newBook(t, "Wrong Year", "Martin", 2015)
	for _, b := range []domain.Book{match, noMatch1, noMatch2} {
		require.NoError(t, repo.Save(ctx, b))
	}

	books, err := repo.List(ctx, repository.ListFilter{
		Author:   ptr("Martin"),
		YearFrom: ptr(2018),
	})

	require.NoError(t, err)
	require.Len(t, books, 1)
	assert.Equal(t, match.Id(), books[0].Id())
}

// ── UpdatedAt advances on mutations ──────────────────────────────────────────

func TestBookRepo_UpdateStatus_AdvancesUpdatedAt(t *testing.T) {
	repo := newFakeBookRepo()
	b := newBook(t, "Title", "Author", 2020)
	require.NoError(t, repo.Save(context.Background(), b))
	before := b.UpdatedAt()
	time.Sleep(time.Millisecond)

	require.NoError(t, repo.UpdateStatus(context.Background(), b.Id(), domain.Indexing))

	got, _ := repo.FindByID(context.Background(), b.Id())
	assert.True(t, got.UpdatedAt().After(before))
}
