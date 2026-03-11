package usecase_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/pkg/domain"
	"github.com/belLena81/raglibrarian/services/metadata/publisher"
	metarepo "github.com/belLena81/raglibrarian/services/metadata/repository"
	"github.com/belLena81/raglibrarian/services/metadata/usecase"
)

// ── fakeBookRepo ──────────────────────────────────────────────────────────────

type fakeBookRepo struct {
	books         map[string]domain.Book
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

func (f *fakeBookRepo) List(_ context.Context, _ metarepo.ListFilter) ([]domain.Book, error) {
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

// ── fakePublisher ─────────────────────────────────────────────────────────────

type fakePublisher struct {
	mu     sync.Mutex
	events []publisher.BookEvent
	err    error
}

func (f *fakePublisher) Publish(_ context.Context, evt publisher.BookEvent) error {
	if f.err != nil {
		return f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, evt)
	return nil
}

func (f *fakePublisher) Close() error { return nil }

func (f *fakePublisher) last() publisher.BookEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.events) == 0 {
		return publisher.BookEvent{}
	}
	return f.events[len(f.events)-1]
}

func (f *fakePublisher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

var _ publisher.BookPublisher = (*fakePublisher)(nil)

// ── helpers ───────────────────────────────────────────────────────────────────

// newBookService creates a BookService with a fresh fake repo and the supplied publisher.
// Pass nil for pub when publisher behaviour is not under test.
func newBookService(t *testing.T, repo metarepo.BookRepository, pub publisher.BookPublisher) *usecase.BookService {
	t.Helper()
	return usecase.NewBookService(repo, pub)
}

// ── Constructor ───────────────────────────────────────────────────────────────

func TestNewBookService_NilRepo_Panics(t *testing.T) {
	assert.Panics(t, func() { usecase.NewBookService(nil, nil) })
}

func TestNewBookService_NilPublisher_DoesNotPanic(t *testing.T) {
	// nil publisher is explicitly allowed — events are silently skipped.
	assert.NotPanics(t, func() { usecase.NewBookService(newFakeBookRepo(), nil) })
}

// ── AddBook ───────────────────────────────────────────────────────────────────

func TestAddBook_Valid_ReturnsPendingBook(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo(), nil)

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

func TestAddBook_PublishesBookCreatedEvent(t *testing.T) {
	pub := &fakePublisher{}
	svc := newBookService(t, newFakeBookRepo(), pub)

	book, err := svc.AddBook(context.Background(), "Clean Code", "Robert Martin", 2008)
	require.NoError(t, err)

	require.Equal(t, 1, pub.count())
	evt := pub.last()
	assert.Equal(t, publisher.EventBookCreated, evt.Event)
	assert.Equal(t, book.Id(), evt.BookID)
	assert.NotEmpty(t, evt.OccurredAt)
	assert.Empty(t, evt.S3Key, "s3_key is not known at creation time")
}

func TestAddBook_NilPublisher_DoesNotPanic(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo(), nil)
	assert.NotPanics(t, func() {
		_, _ = svc.AddBook(context.Background(), "Title", "Author", 2020)
	})
}

func TestAddBook_PublishError_DoesNotRollBackSave(t *testing.T) {
	// Publish failures are best-effort; the book must still be persisted.
	pub := &fakePublisher{err: assert.AnError}
	repo := newFakeBookRepo()
	svc := newBookService(t, repo, pub)

	book, err := svc.AddBook(context.Background(), "Title", "Author", 2020)

	require.NoError(t, err, "save must succeed even when publish fails")
	_, getErr := svc.GetBook(context.Background(), book.Id())
	assert.NoError(t, getErr, "book must be retrievable after failed publish")
}

func TestAddBook_InvalidTitle_ReturnsDomainError(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo(), nil)
	_, err := svc.AddBook(context.Background(), "", "Author", 2020)
	assert.ErrorIs(t, err, domain.ErrEmptyTitle)
}

func TestAddBook_InvalidAuthor_ReturnsDomainError(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo(), nil)
	_, err := svc.AddBook(context.Background(), "Title", "", 2020)
	assert.ErrorIs(t, err, domain.ErrEmptyAuthor)
}

func TestAddBook_InvalidYear_ReturnsDomainError(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo(), nil)
	_, err := svc.AddBook(context.Background(), "Title", "Author", 1800)
	assert.ErrorIs(t, err, domain.ErrInvalidYear)
}

func TestAddBook_Duplicate_ReturnsDuplicateBook(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo(), nil)
	_, err := svc.AddBook(context.Background(), "Clean Code", "Robert Martin", 2008)
	require.NoError(t, err)

	_, err = svc.AddBook(context.Background(), "Clean Code", "Robert Martin", 2008)
	assert.ErrorIs(t, err, domain.ErrDuplicateBook)
}

func TestAddBook_SameTitleDifferentYear_Succeeds(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo(), nil)
	_, err := svc.AddBook(context.Background(), "The Pragmatic Programmer", "Hunt & Thomas", 1999)
	require.NoError(t, err)

	_, err = svc.AddBook(context.Background(), "The Pragmatic Programmer", "Hunt & Thomas", 2019)
	assert.NoError(t, err)
}

func TestAddBook_RepoError_ReturnsError_NoEvent(t *testing.T) {
	pub := &fakePublisher{}
	repo := newFakeBookRepo()
	repo.saveErr = assert.AnError
	svc := newBookService(t, repo, pub)

	_, err := svc.AddBook(context.Background(), "Title", "Author", 2020)

	require.ErrorIs(t, err, assert.AnError)
	assert.Equal(t, 0, pub.count(), "no event must be published when save fails")
}

// ── GetBook ───────────────────────────────────────────────────────────────────

func TestGetBook_Exists_ReturnsBook(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo(), nil)
	added, err := svc.AddBook(context.Background(), "DDIA", "Kleppmann", 2017)
	require.NoError(t, err)

	got, err := svc.GetBook(context.Background(), added.Id())

	require.NoError(t, err)
	assert.Equal(t, added.Id(), got.Id())
	assert.Equal(t, "DDIA", got.Title())
}

func TestGetBook_Missing_ReturnsBookNotFound(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo(), nil)
	_, err := svc.GetBook(context.Background(), "ghost-id")
	assert.ErrorIs(t, err, domain.ErrBookNotFound)
}

func TestGetBook_EmptyID_ReturnsDomainError(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo(), nil)
	_, err := svc.GetBook(context.Background(), "")
	assert.ErrorIs(t, err, domain.ErrEmptyBookID)
}

// ── ListBooks ─────────────────────────────────────────────────────────────────

func TestListBooks_Empty_ReturnsNonNilEmptySlice(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo(), nil)

	books, err := svc.ListBooks(context.Background(), metarepo.ListFilter{})

	require.NoError(t, err)
	assert.NotNil(t, books)
	assert.Empty(t, books)
}

func TestListBooks_ReturnsAll(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo(), nil)
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
	svc := newBookService(t, newFakeBookRepo(), nil)
	added, err := svc.AddBook(context.Background(), "Title", "Author", 2020)
	require.NoError(t, err)

	require.NoError(t, svc.RemoveBook(context.Background(), added.Id()))

	_, err = svc.GetBook(context.Background(), added.Id())
	assert.ErrorIs(t, err, domain.ErrBookNotFound)
}

func TestRemoveBook_Missing_ReturnsBookNotFound(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo(), nil)
	assert.ErrorIs(t, svc.RemoveBook(context.Background(), "ghost-id"), domain.ErrBookNotFound)
}

func TestRemoveBook_EmptyID_ReturnsDomainError(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo(), nil)
	assert.ErrorIs(t, svc.RemoveBook(context.Background(), ""), domain.ErrEmptyBookID)
}

// ── TriggerReindex ────────────────────────────────────────────────────────────

func TestTriggerReindex_FromIndexed_ResetsToPending_PublishesEvent(t *testing.T) {
	pub := &fakePublisher{}
	repo := newFakeBookRepo()
	svc := newBookService(t, repo, pub)
	added, err := svc.AddBook(context.Background(), "Title", "Author", 2020)
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(context.Background(), added.Id(), domain.Indexing))
	require.NoError(t, repo.UpdateStatus(context.Background(), added.Id(), domain.Indexed))

	require.NoError(t, svc.TriggerReindex(context.Background(), added.Id()))

	got, err := svc.GetBook(context.Background(), added.Id())
	require.NoError(t, err)
	assert.Equal(t, domain.Pending, got.Status())

	// One EventBookCreated (from AddBook) + one EventBookReindexRequested.
	require.Equal(t, 2, pub.count())
	evt := pub.last()
	assert.Equal(t, publisher.EventBookReindexRequested, evt.Event)
	assert.Equal(t, added.Id(), evt.BookID)
	assert.NotEmpty(t, evt.OccurredAt)
}

func TestTriggerReindex_S3KeyIncludedInEvent(t *testing.T) {
	pub := &fakePublisher{}
	repo := newFakeBookRepo()
	svc := newBookService(t, repo, pub)
	added, err := svc.AddBook(context.Background(), "Title", "Author", 2020)
	require.NoError(t, err)
	require.NoError(t, repo.UpdateS3Key(context.Background(), added.Id(), "books/b-1/file.pdf"))
	require.NoError(t, repo.UpdateStatus(context.Background(), added.Id(), domain.Indexing))
	require.NoError(t, repo.UpdateStatus(context.Background(), added.Id(), domain.Indexed))

	require.NoError(t, svc.TriggerReindex(context.Background(), added.Id()))

	evt := pub.last()
	assert.Equal(t, "books/b-1/file.pdf", evt.S3Key)
}

func TestTriggerReindex_FromFailed_ResetsToPending(t *testing.T) {
	repo := newFakeBookRepo()
	svc := newBookService(t, repo, nil)
	added, err := svc.AddBook(context.Background(), "Title", "Author", 2020)
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(context.Background(), added.Id(), domain.Indexing))
	require.NoError(t, repo.UpdateStatus(context.Background(), added.Id(), domain.Failed))

	require.NoError(t, svc.TriggerReindex(context.Background(), added.Id()))

	got, _ := svc.GetBook(context.Background(), added.Id())
	assert.Equal(t, domain.Pending, got.Status())
}

func TestTriggerReindex_FromPending_ReturnsTransitionError_NoEvent(t *testing.T) {
	pub := &fakePublisher{}
	svc := newBookService(t, newFakeBookRepo(), pub)
	added, err := svc.AddBook(context.Background(), "Title", "Author", 2020)
	require.NoError(t, err)
	initialCount := pub.count()

	err = svc.TriggerReindex(context.Background(), added.Id())

	assert.ErrorIs(t, err, domain.ErrInvalidStatusTransition)
	assert.Equal(t, initialCount, pub.count(), "no reindex event on failed transition")
}

func TestTriggerReindex_FromIndexing_ReturnsTransitionError(t *testing.T) {
	repo := newFakeBookRepo()
	svc := newBookService(t, repo, nil)
	added, err := svc.AddBook(context.Background(), "Title", "Author", 2020)
	require.NoError(t, err)
	require.NoError(t, repo.UpdateStatus(context.Background(), added.Id(), domain.Indexing))

	err = svc.TriggerReindex(context.Background(), added.Id())
	assert.ErrorIs(t, err, domain.ErrInvalidStatusTransition)
}

func TestTriggerReindex_Missing_ReturnsBookNotFound(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo(), nil)
	assert.ErrorIs(t, svc.TriggerReindex(context.Background(), "ghost-id"), domain.ErrBookNotFound)
}

func TestTriggerReindex_EmptyID_ReturnsDomainError(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo(), nil)
	assert.ErrorIs(t, svc.TriggerReindex(context.Background(), ""), domain.ErrEmptyBookID)
}

// ── UpdateStatus ─────────────────────────────────────────────────────────

func TestUpdateStatus_ValidTransition_Succeeds(t *testing.T) {
	repo := newFakeBookRepo()
	svc := newBookService(t, repo, nil)
	added, err := svc.AddBook(context.Background(), "Title", "Author", 2020)
	require.NoError(t, err)

	require.NoError(t, svc.UpdateStatus(context.Background(), added.Id(), domain.Indexing))

	got, _ := svc.GetBook(context.Background(), added.Id())
	assert.Equal(t, domain.Indexing, got.Status())
}

func TestUpdateStatus_InvalidTransition_ReturnsDomainError(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo(), nil)
	added, err := svc.AddBook(context.Background(), "Title", "Author", 2020)
	require.NoError(t, err)

	// pending → indexed is forbidden.
	err = svc.UpdateStatus(context.Background(), added.Id(), domain.Indexed)

	assert.ErrorIs(t, err, domain.ErrInvalidStatusTransition)
}

func TestUpdateStatus_InvalidStatus_ReturnsDomainError(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo(), nil)
	added, err := svc.AddBook(context.Background(), "Title", "Author", 2020)
	require.NoError(t, err)

	err = svc.UpdateStatus(context.Background(), added.Id(), domain.Status(7))
	assert.ErrorIs(t, err, domain.ErrInvalidStatus)
}

func TestUpdateStatus_Missing_ReturnsBookNotFound(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo(), nil)
	assert.ErrorIs(t, svc.UpdateStatus(context.Background(), "ghost-id", domain.Indexing), domain.ErrBookNotFound)
}

func TestUpdateStatus_EmptyID_ReturnsDomainError(t *testing.T) {
	svc := newBookService(t, newFakeBookRepo(), nil)
	assert.ErrorIs(t, svc.UpdateStatus(context.Background(), "", domain.Indexing), domain.ErrEmptyBookID)
}
