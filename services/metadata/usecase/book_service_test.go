package usecase_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/pkg/domain"
	metarepo "github.com/belLena81/raglibrarian/services/metadata/repository"
	"github.com/belLena81/raglibrarian/services/metadata/usecase"
)

// ── fakeBookRepo ──────────────────────────────────────────────────────────────

type fakeBookRepo struct {
	books         map[string]domain.Book // keyed by id
	saveErr       error
	findErr       error
	deleteErr     error
	updateStatErr error
	updateKeyErr  error
}

func newFakeBookRepo() *fakeBookRepo {
	return &fakeBookRepo{books: make(map[string]domain.Book)}
}

func (f *fakeBookRepo) Save(_ context.Context, b domain.Book) error {
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
	if f.findErr != nil {
		return domain.Book{}, f.findErr
	}
	b, ok := f.books[id]
	if !ok {
		return domain.Book{}, domain.ErrBookNotFound
	}
	return b, nil
}

func (f *fakeBookRepo) List(_ context.Context, filter metarepo.ListFilter) ([]domain.Book, error) {
	result := make([]domain.Book, 0, len(f.books))
	for _, b := range f.books {
		result = append(result, b)
	}
	return result, nil
}

func (f *fakeBookRepo) Delete(_ context.Context, id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, ok := f.books[id]; !ok {
		return domain.ErrBookNotFound
	}
	delete(f.books, id)
	return nil
}

func (f *fakeBookRepo) UpdateStatus(_ context.Context, id string, next domain.Status) error {
	if f.updateStatErr != nil {
		return f.updateStatErr
	}
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
	if f.updateKeyErr != nil {
		return f.updateKeyErr
	}
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

var _ metarepo.BookRepository = (*fakeBookRepo)(nil)

// ── helpers ───────────────────────────────────────────────────────────────────

func newBookService(t *testing.T, repo metarepo.BookRepository) *usecase.BookService {
	t.Helper()
	return usecase.NewBookService(repo)
}

// ── Constructor ───────────────────────────────────────────────────────────────

func TestNewBookService_NilRepo_Panics(t *testing.T) {
	assert.Panics(t, func() { usecase.NewBookService(nil) })
}

// ── AddBook ───────────────────────────────────────────────────────────────────

func TestAddBook_Valid_ReturnsPendingBook(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo())

	book, err := svc.AddBook(context.Background(), "Clean Code", "Robert Martin", 2008)

	require.NoError(t, err)
	assert.NotEmpty(t, book.Id())
	assert.Equal(t, "Clean Code", book.Title())
	assert.Equal(t, "Robert Martin", book.Author())
	assert.Equal(t, 2008, book.Year())
	assert.Equal(t, domain.Pending, book.Status())
	assert.Empty(t, book.S3Key())
	assert.NotNil(t, book.Tags())
}

func TestAddBook_InvalidTitle_ReturnsDomainError(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo())

	_, err := svc.AddBook(context.Background(), "", "Author", 2020)

	assert.ErrorIs(t, err, domain.ErrEmptyTitle)
}

func TestAddBook_InvalidAuthor_ReturnsDomainError(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo())

	_, err := svc.AddBook(context.Background(), "Title", "", 2020)

	assert.ErrorIs(t, err, domain.ErrEmptyAuthor)
}

func TestAddBook_InvalidYear_ReturnsDomainError(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo())

	_, err := svc.AddBook(context.Background(), "Title", "Author", 1800)

	assert.ErrorIs(t, err, domain.ErrInvalidYear)
}

func TestAddBook_Duplicate_ReturnsDuplicateBook(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo())
	_, err := svc.AddBook(context.Background(), "Clean Code", "Robert Martin", 2008)
	require.NoError(t, err)

	_, err = svc.AddBook(context.Background(), "Clean Code", "Robert Martin", 2008)

	assert.ErrorIs(t, err, domain.ErrDuplicateBook)
}

func TestAddBook_SameTitleDifferentYear_Succeeds(t *testing.T) {
	// Different edition — not a duplicate.
	svc := newBookService(t, newFakeBookRepo())
	_, err := svc.AddBook(context.Background(), "The Pragmatic Programmer", "Hunt & Thomas", 1999)
	require.NoError(t, err)

	_, err = svc.AddBook(context.Background(), "The Pragmatic Programmer", "Hunt & Thomas", 2019)

	assert.NoError(t, err)
}

func TestAddBook_RepoError_WrappedAndReturned(t *testing.T) {
	repo := newFakeBookRepo()
	repo.saveErr = assert.AnError
	svc := newBookService(t, repo)

	_, err := svc.AddBook(context.Background(), "Title", "Author", 2020)

	require.Error(t, err)
	assert.ErrorIs(t, err, assert.AnError)
}

// ── GetBook ───────────────────────────────────────────────────────────────────

func TestGetBook_Exists_ReturnsBook(t *testing.T) {
	repo := newFakeBookRepo()
	svc := newBookService(t, repo)
	added, err := svc.AddBook(context.Background(), "DDIA", "Kleppmann", 2017)
	require.NoError(t, err)

	got, err := svc.GetBook(context.Background(), added.Id())

	require.NoError(t, err)
	assert.Equal(t, added.Id(), got.Id())
	assert.Equal(t, "DDIA", got.Title())
}

func TestGetBook_Missing_ReturnsBookNotFound(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo())

	_, err := svc.GetBook(context.Background(), "ghost-id")

	assert.ErrorIs(t, err, domain.ErrBookNotFound)
}

func TestGetBook_EmptyID_ReturnsDomainError(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo())

	_, err := svc.GetBook(context.Background(), "")

	assert.ErrorIs(t, err, domain.ErrEmptyBookID)
}

// ── ListBooks ─────────────────────────────────────────────────────────────────

func TestListBooks_Empty_ReturnsNonNilEmptySlice(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo())

	books, err := svc.ListBooks(context.Background(), metarepo.ListFilter{})

	require.NoError(t, err)
	assert.NotNil(t, books)
	assert.Empty(t, books)
}

func TestListBooks_ReturnsAll(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo())
	_, err := svc.AddBook(context.Background(), "Book A", "Author A", 2020)
	require.NoError(t, err)
	_, err = svc.AddBook(context.Background(), "Book B", "Author B", 2021)
	require.NoError(t, err)

	books, err := svc.ListBooks(context.Background(), metarepo.ListFilter{})

	require.NoError(t, err)
	assert.Len(t, books, 2)
}

// ── RemoveBook ────────────────────────────────────────────────────────────────

func TestRemoveBook_Exists_DeletesBook(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo())
	added, err := svc.AddBook(context.Background(), "Title", "Author", 2020)
	require.NoError(t, err)

	require.NoError(t, svc.RemoveBook(context.Background(), added.Id()))

	_, err = svc.GetBook(context.Background(), added.Id())
	assert.ErrorIs(t, err, domain.ErrBookNotFound)
}

func TestRemoveBook_Missing_ReturnsBookNotFound(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo())

	err := svc.RemoveBook(context.Background(), "ghost-id")

	assert.ErrorIs(t, err, domain.ErrBookNotFound)
}

func TestRemoveBook_EmptyID_ReturnsDomainError(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo())

	err := svc.RemoveBook(context.Background(), "")

	assert.ErrorIs(t, err, domain.ErrEmptyBookID)
}

// ── TriggerReindex ────────────────────────────────────────────────────────────

func TestTriggerReindex_FromIndexed_ResetsToPending(t *testing.T) {
	repo := newFakeBookRepo()
	svc := newBookService(t, repo)
	added, err := svc.AddBook(context.Background(), "Title", "Author", 2020)
	require.NoError(t, err)
	// Advance to indexed via repo directly (bypasses use case — tests repo state).
	require.NoError(t, repo.UpdateStatus(context.Background(), added.Id(), domain.Indexing))
	require.NoError(t, repo.UpdateStatus(context.Background(), added.Id(), domain.Indexed))

	require.NoError(t, svc.TriggerReindex(context.Background(), added.Id()))

	got, err := svc.GetBook(context.Background(), added.Id())
	require.NoError(t, err)
	assert.Equal(t, domain.Pending, got.Status())
}

func TestTriggerReindex_FromFailed_ResetsToPending(t *testing.T) {
	repo := newFakeBookRepo()
	svc := newBookService(t, repo)
	added, err := svc.AddBook(context.Background(), "Title", "Author", 2020)
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(context.Background(), added.Id(), domain.Indexing))
	require.NoError(t, repo.UpdateStatus(context.Background(), added.Id(), domain.Failed))

	require.NoError(t, svc.TriggerReindex(context.Background(), added.Id()))

	got, _ := svc.GetBook(context.Background(), added.Id())
	assert.Equal(t, domain.Pending, got.Status())
}

func TestTriggerReindex_FromPending_ReturnsTransitionError(t *testing.T) {
	// pending → pending is not a valid reindex — the book hasn't been indexed yet.
	svc := newBookService(t, newFakeBookRepo())
	added, err := svc.AddBook(context.Background(), "Title", "Author", 2020)
	require.NoError(t, err)

	err = svc.TriggerReindex(context.Background(), added.Id())

	assert.ErrorIs(t, err, domain.ErrInvalidStatusTransition)
}

func TestTriggerReindex_FromIndexing_ReturnsTransitionError(t *testing.T) {
	// Cannot reindex a book that is currently being indexed.
	repo := newFakeBookRepo()
	svc := newBookService(t, repo)
	added, err := svc.AddBook(context.Background(), "Title", "Author", 2020)
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(context.Background(), added.Id(), domain.Indexing))

	err = svc.TriggerReindex(context.Background(), added.Id())

	assert.ErrorIs(t, err, domain.ErrInvalidStatusTransition)
}

func TestTriggerReindex_Missing_ReturnsBookNotFound(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo())

	err := svc.TriggerReindex(context.Background(), "ghost-id")

	assert.ErrorIs(t, err, domain.ErrBookNotFound)
}

func TestTriggerReindex_EmptyID_ReturnsDomainError(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo())

	err := svc.TriggerReindex(context.Background(), "")

	assert.ErrorIs(t, err, domain.ErrEmptyBookID)
}

// ── UpdateStatus ─────────────────────────────────────────────────────────

func TestUpdateStatus_ValidTransition_Succeeds(t *testing.T) {
	repo := newFakeBookRepo()
	svc := newBookService(t, repo)
	added, err := svc.AddBook(context.Background(), "Title", "Author", 2020)
	require.NoError(t, err)

	require.NoError(t, svc.UpdateStatus(context.Background(), added.Id(), domain.Indexing))

	got, _ := svc.GetBook(context.Background(), added.Id())
	assert.Equal(t, domain.Indexing, got.Status())
}

func TestUpdateStatus_InvalidTransition_ReturnsDomainError(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo())
	added, err := svc.AddBook(context.Background(), "Title", "Author", 2020)
	require.NoError(t, err)

	// pending → indexed is forbidden.
	err = svc.UpdateStatus(context.Background(), added.Id(), domain.Indexed)

	assert.ErrorIs(t, err, domain.ErrInvalidStatusTransition)
}

func TestUpdateStatus_InvalidStatus_ReturnsDomainError(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo())
	added, err := svc.AddBook(context.Background(), "Title", "Author", 2020)
	require.NoError(t, err)

	err = svc.UpdateStatus(context.Background(), added.Id(), domain.Status(7))

	assert.ErrorIs(t, err, domain.ErrInvalidStatus)
}

func TestUpdateStatus_Missing_ReturnsBookNotFound(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo())

	err := svc.UpdateStatus(context.Background(), "ghost-id", domain.Indexing)

	assert.ErrorIs(t, err, domain.ErrBookNotFound)
}

func TestUpdateStatus_EmptyID_ReturnsDomainError(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo())

	err := svc.UpdateStatus(context.Background(), "", domain.Indexing)

	assert.ErrorIs(t, err, domain.ErrEmptyBookID)
}
