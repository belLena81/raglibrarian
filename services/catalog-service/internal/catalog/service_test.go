package catalog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestUploadBookStoresPendingPDF(t *testing.T) {
	repository := NewMemoryRepository()
	objects := NewMemoryObjectStore()
	service := NewService(repository, objects, 1024)

	book, err := service.UploadBook(context.Background(), UploadInput{
		Metadata: BookMetadata{Title: "A title", Author: "An author", Year: 2026, Tags: []string{"go"}},
		Actor:    Actor{UserID: "actor-1", Role: "librarian", Status: "active"}, Reader: bytes.NewBufferString("%PDF-1.7\nbody"),
	})

	if err != nil {
		t.Fatal(err)
	}
	if book.ProcessingStatus != BookStatusPending {
		t.Fatalf("status = %q", book.ProcessingStatus)
	}
	if len(objects.objects) != 1 {
		t.Fatalf("objects = %d", len(objects.objects))
	}
}

func TestUploadBookNormalizesAbsentTagsToEmptyArray(t *testing.T) {
	service := NewService(NewMemoryRepository(), NewMemoryObjectStore(), 1024)
	book, err := service.UploadBook(context.Background(), UploadInput{
		Metadata: BookMetadata{Title: "A title", Author: "An author", Year: 2026},
		Actor:    Actor{UserID: "actor-1", Role: "librarian", Status: "active"},
		Reader:   bytes.NewBufferString("%PDF-1.7\nbody"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if book.Metadata.Tags == nil || len(book.Metadata.Tags) != 0 {
		t.Fatalf("tags = %#v", book.Metadata.Tags)
	}
}

func TestUploadBookRejectsInactiveOrReaderActor(t *testing.T) {
	service := NewService(NewMemoryRepository(), NewMemoryObjectStore(), 1024)
	for _, actor := range []Actor{
		{UserID: "reader", Role: "reader", Status: "active"},
		{UserID: "librarian", Role: "librarian", Status: "pending"},
	} {
		_, err := service.UploadBook(context.Background(), UploadInput{
			Metadata: BookMetadata{Title: "Title", Author: "Author", Year: 2026},
			Actor:    actor,
			Reader:   bytes.NewBufferString("%PDF-1.7\nbody"),
		})
		if !errors.Is(err, ErrUnauthorizedActor) {
			t.Fatalf("actor %+v error = %v", actor, err)
		}
	}
}

func TestUploadBookCapacityIncludesReadersBlockedBeforeFirstByte(t *testing.T) {
	service := NewServiceWithOptions(NewMemoryRepository(), NewMemoryObjectStore(), ServiceOptions{
		MaxBytes:          1024,
		UploadConcurrency: 1,
	})
	firstReader := newBlockedReader("%PDF-1.7\nfirst")
	firstResult := make(chan error, 1)
	t.Cleanup(func() {
		firstReader.unblock()
	})
	go func() {
		_, err := service.UploadBook(context.Background(), validUploadInput(firstReader))
		firstResult <- err
	}()

	select {
	case <-firstReader.started:
	case <-time.After(time.Second):
		t.Fatal("first upload did not start reading")
	}
	secondReader := &countingReader{reader: strings.NewReader("%PDF-1.7\nsecond")}
	_, err := service.UploadBook(context.Background(), validUploadInput(secondReader))
	if !errors.Is(err, ErrUploadCapacity) {
		t.Fatalf("second upload error = %v, want %v", err, ErrUploadCapacity)
	}
	if reads := secondReader.reads.Load(); reads != 0 {
		t.Fatalf("second reader reads = %d, want 0", reads)
	}

	firstReader.unblock()
	select {
	case err = <-firstResult:
		if err != nil {
			t.Fatalf("first upload error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first upload did not complete after reader release")
	}
	if _, err = service.UploadBook(context.Background(), validUploadInput(strings.NewReader("%PDF-1.7\nthird"))); err != nil {
		t.Fatalf("upload after release error = %v", err)
	}
}

func validUploadInput(reader io.Reader) UploadInput {
	return UploadInput{
		Metadata: BookMetadata{Title: "Title", Author: "Author", Year: 2026},
		Actor:    Actor{UserID: "actor", Role: "librarian", Status: "active"},
		Reader:   reader,
	}
}

type blockedReader struct {
	reader   io.Reader
	started  chan struct{}
	release  chan struct{}
	once     sync.Once
	released sync.Once
}

func (r *blockedReader) unblock() {
	r.released.Do(func() {
		close(r.release)
	})
}

func newBlockedReader(body string) *blockedReader {
	return &blockedReader{
		reader:  strings.NewReader(body),
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (r *blockedReader) Read(buffer []byte) (int, error) {
	r.once.Do(func() {
		close(r.started)
		<-r.release
	})
	return r.reader.Read(buffer)
}

type countingReader struct {
	reader io.Reader
	reads  atomic.Int32
}

func (r *countingReader) Read(buffer []byte) (int, error) {
	r.reads.Add(1)
	return r.reader.Read(buffer)
}

func TestMemoryRepositoryUsesNewestFirstTimestampAndIDCursor(t *testing.T) {
	repository := NewMemoryRepository()
	createdAt := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"a", "c", "b"} {
		if err := repository.Create(context.Background(), Book{ID: id, CreatedAt: createdAt}, OutboxEvent{}); err != nil {
			t.Fatal(err)
		}
	}
	first, cursor, err := repository.List(context.Background(), 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].ID != "c" || first[1].ID != "b" || cursor == "" {
		t.Fatalf("first page = %#v, cursor = %q", first, cursor)
	}
	second, next, err := repository.List(context.Background(), 2, cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].ID != "a" || next != "" {
		t.Fatalf("second page = %#v, next = %q", second, next)
	}
}

func TestUploadBookRejectsSpoofedPDFAndCompensates(t *testing.T) {
	objects := NewMemoryObjectStore()
	service := NewService(NewMemoryRepository(), objects, 1024)
	_, err := service.UploadBook(context.Background(), UploadInput{Metadata: BookMetadata{Title: "Title", Author: "Author", Year: 2026}, Actor: Actor{UserID: "actor", Role: "librarian", Status: "active"}, Reader: bytes.NewBufferString("not a pdf")})
	if !errors.Is(err, ErrInvalidPDF) {
		t.Fatalf("error = %v", err)
	}
	if len(objects.objects) != 0 {
		t.Fatalf("objects = %d", len(objects.objects))
	}
}

func TestUploadBookEnforcesSizeLimit(t *testing.T) {
	service := NewService(NewMemoryRepository(), NewMemoryObjectStore(), 5)
	_, err := service.UploadBook(context.Background(), UploadInput{Metadata: BookMetadata{Title: "Title", Author: "Author", Year: 2026}, Actor: Actor{UserID: "actor", Role: "librarian", Status: "active"}, Reader: bytes.NewBufferString("%PDF-too-large")})
	if !errors.Is(err, ErrUploadTooLarge) {
		t.Fatalf("error = %v", err)
	}
}

func TestUploadBookDeletesObjectWhenStorageReceiptDoesNotMatch(t *testing.T) {
	objects := &receiptMismatchObjectStore{objects: NewMemoryObjectStore()}
	service := NewService(NewMemoryRepository(), objects, 1024)
	_, err := service.UploadBook(context.Background(), UploadInput{
		Metadata: BookMetadata{Title: "Title", Author: "Author", Year: 2026},
		Actor:    Actor{UserID: "actor", Role: "librarian", Status: "active"},
		Reader:   bytes.NewBufferString("%PDF-1.7\nbody"),
	})
	if !errors.Is(err, ErrObjectReceiptMismatch) {
		t.Fatalf("error = %v", err)
	}
	if len(objects.objects.objects) != 0 {
		t.Fatalf("objects = %d", len(objects.objects.objects))
	}
}

func TestSanitizeUploadErrorPreservesStorageSentinels(t *testing.T) {
	for _, testCase := range []struct {
		name string
		err  error
		want error
	}{
		{name: "receipt mismatch", err: fmt.Errorf("wrapped: %w", ErrObjectReceiptMismatch), want: ErrObjectReceiptMismatch},
		{name: "storage unavailable", err: fmt.Errorf("wrapped: %w", ErrObjectStorageUnavailable), want: ErrObjectStorageUnavailable},
		{name: "unknown storage error", err: errors.New("minio object originals/private.pdf unavailable"), want: ErrObjectStorageUnavailable},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := sanitizeUploadError(testCase.err); !errors.Is(got, testCase.want) {
				t.Fatalf("sanitizeUploadError() = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestUploadBookCompensatesAndSanitizesStorageUnavailable(t *testing.T) {
	objects := &unavailableObjectStore{}
	service := NewService(NewMemoryRepository(), objects, 1024)
	_, err := service.UploadBook(context.Background(), UploadInput{
		Metadata: BookMetadata{Title: "Title", Author: "Author", Year: 2026},
		Actor:    Actor{UserID: "actor", Role: "librarian", Status: "active"},
		Reader:   bytes.NewBufferString("%PDF-1.7\nbody"),
	})
	if !errors.Is(err, ErrObjectStorageUnavailable) {
		t.Fatalf("error = %v", err)
	}
	if !objects.deleted {
		t.Fatal("storage failure was not compensated")
	}
	if strings.Contains(err.Error(), "originals/") {
		t.Fatal("storage error exposed object reference")
	}
}

func TestUploadBookPreservesObjectAfterAmbiguousCommittedCreate(t *testing.T) {
	objects := NewMemoryObjectStore()
	service := NewServiceWithOptions(&ambiguousCreateRepository{}, objects, ServiceOptions{
		MaxBytes: 1024,
		NewID: func() (string, error) {
			return "fixed-id", nil
		},
	})
	book, err := service.UploadBook(context.Background(), UploadInput{
		Metadata: BookMetadata{Title: "Title", Author: "Author", Year: 2026},
		Actor:    Actor{UserID: "actor", Role: "librarian", Status: "active"},
		Reader:   bytes.NewBufferString("%PDF-1.7\nbody"),
	})
	if err != nil {
		t.Fatalf("UploadBook() error = %v", err)
	}
	if book.ID != "fixed-id" {
		t.Fatalf("book ID = %q", book.ID)
	}
	if len(objects.objects) != 1 {
		t.Fatalf("objects = %d, want preserved object", len(objects.objects))
	}
}

type ambiguousCreateRepository struct{}

func (ambiguousCreateRepository) Create(context.Context, Book, ...OutboxEvent) error {
	return errors.New("connection lost after commit")
}

func (ambiguousCreateRepository) List(context.Context, int, string) ([]Book, string, error) {
	return nil, "", nil
}

func (ambiguousCreateRepository) Get(context.Context, string) (Book, error) {
	return Book{ID: "fixed-id"}, nil
}

type receiptMismatchObjectStore struct{ objects *MemoryObjectStore }

func (s *receiptMismatchObjectStore) Put(ctx context.Context, key string, reader io.Reader) (ObjectReceipt, error) {
	receipt, err := s.objects.Put(ctx, key, reader)
	return ObjectReceipt{Size: receipt.Size + 1}, err
}

func (s *receiptMismatchObjectStore) Delete(ctx context.Context, key string) error {
	return s.objects.Delete(ctx, key)
}

type unavailableObjectStore struct{ deleted bool }

func (s *unavailableObjectStore) Put(context.Context, string, io.Reader) (ObjectReceipt, error) {
	return ObjectReceipt{}, fmt.Errorf("minio originals/private.pdf: %w", ErrObjectStorageUnavailable)
}

func (s *unavailableObjectStore) Delete(context.Context, string) error {
	s.deleted = true
	return nil
}

func TestListBooksRejectsMalformedCursorAsPagination(t *testing.T) {
	service := NewService(NewMemoryRepository(), NewMemoryObjectStore(), 1024)
	_, _, err := service.ListBooks(context.Background(), 25, "not-a-cursor")
	if !errors.Is(err, ErrInvalidPagination) {
		t.Fatalf("error = %v", err)
	}
}
