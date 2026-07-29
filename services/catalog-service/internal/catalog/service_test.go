package catalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/belLena81/raglibrarian/services/catalog-service/config"
)

const (
	testServiceMaxPreviewBytes = 1 << 20
	testServiceMaxPreviewPages = 3
	testServiceMaxEPUBEntries  = 2048
	testCorrelationID          = "0123456789abcdef0123456789abcdef"
)

func TestDefaultUploadEnvelopeMatchesM4SourceProfile(t *testing.T) {
	cfg := mustLoadCatalogConfigForTest(t)
	if cfg.MaxUploadBytes != 25<<20 {
		t.Fatalf("MaxUploadBytes = %d, want %d", cfg.MaxUploadBytes, 25<<20)
	}
}

func TestNewServiceWithOptionsRequiresPositiveMaxBytes(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for invalid max upload bytes")
		}
	}()
	_ = NewServiceWithOptions(NewMemoryRepository(), NewMemoryObjectStore(), ServiceOptions{})
}

func mustLoadCatalogConfigForTest(t *testing.T) config.Config {
	t.Helper()
	setCatalogTestEnvironment(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func setCatalogTestEnvironment(t *testing.T) {
	t.Helper()
	tempDir := t.TempDir()
	writeSecret := func(name, value string) string {
		t.Helper()
		path := tempDir + "/" + name
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	t.Setenv("CATALOG_POSTGRES_DSN_FILE", writeSecret("dsn", "postgres://catalog:catalog@db.internal/catalog?sslmode=disable"))
	t.Setenv("CATALOG_MINIO_ACCESS_KEY_FILE", writeSecret("minio-access", "access"))
	t.Setenv("CATALOG_MINIO_SECRET_KEY_FILE", writeSecret("minio-secret", "secret"))
	t.Setenv("CATALOG_RABBITMQ_URI_FILE", writeSecret("rabbit", "amqps://catalog:secret@rabbit.internal:5671/catalog"))
	t.Setenv("CATALOG_INGESTION_RABBITMQ_URI_FILE", writeSecret("rabbit-ingestion", "amqps://catalog:secret@rabbit.internal:5671/catalog"))
	t.Setenv("CATALOG_RETRIEVAL_RABBITMQ_URI_FILE", writeSecret("rabbit-retrieval", "amqps://catalog:secret@rabbit.internal:5671/catalog"))
	t.Setenv("INTERNAL_TLS_CA_FILE", writeSecret("ca.pem", "test-ca"))
	t.Setenv("CATALOG_TLS_CERT_FILE", writeSecret("cert.pem", "test-cert"))
	t.Setenv("CATALOG_TLS_KEY_FILE", writeSecret("key.pem", "test-key"))
	t.Setenv("CATALOG_MINIO_ENDPOINT", "minio.internal:9000")
	t.Setenv("CATALOG_MINIO_BUCKET", "books")
	t.Setenv("CATALOG_GRPC_ADDR", ":50052")
	t.Setenv("CATALOG_METRICS_ADDR", "127.0.0.1:9090")
}

func TestUploadBookStoresPendingPDF(t *testing.T) {
	repository := NewMemoryRepository()
	objects := NewMemoryObjectStore()
	service := NewService(repository, objects, 1024)

	book, err := service.UploadBook(context.Background(), UploadInput{
		Metadata:      BookMetadata{Title: "A title", Author: "An author", Year: 2026, Tags: []string{"go"}},
		Actor:         Actor{UserID: "actor-1", Role: "librarian", Status: "active"},
		CorrelationID: testCorrelationID,
		Reader:        bytes.NewBufferString("%PDF-1.7\nbody"),
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

func TestUploadBookStoresEPUBWithLifecycleProjection(t *testing.T) {
	repository := NewMemoryRepository()
	objects := NewMemoryObjectStore()
	service := NewService(repository, objects, 1024)

	book, err := service.UploadBook(context.Background(), UploadInput{
		Metadata:      BookMetadata{Title: "An EPUB", Author: "An author", Year: 2026},
		MediaType:     "application/epub+zip",
		Actor:         Actor{UserID: "actor-1", Role: "librarian", Status: "active"},
		CorrelationID: testCorrelationID,
		Reader:        bytes.NewReader([]byte{'P', 'K', 3, 4, 'b', 'o', 'd', 'y'}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if book.MediaType != "application/epub+zip" || book.LifecycleVersion != 1 {
		t.Fatalf("book = %+v", book)
	}
	if !strings.HasSuffix(book.ObjectReference, ".epub") {
		t.Fatalf("object reference = %q", book.ObjectReference)
	}
}

func TestLifecycleCommandsAreIdempotentAndDeletionWaitsForAllCleanup(t *testing.T) {
	repository := NewMemoryRepository()
	objects := NewMemoryObjectStore()
	service := NewServiceWithOptions(repository, objects, ServiceOptions{
		MaxBytes: 1024,
		Clock: func() time.Time {
			return time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
		},
		NewID: func() (string, error) {
			return "event-id", nil
		},
	})
	book, err := service.UploadBook(context.Background(), validUploadInput(strings.NewReader("%PDF-1.7\nbody")))
	if err != nil {
		t.Fatal(err)
	}
	book.ProcessingStatus = BookStatusIndexed
	book.ProcessingStage = BookStageIndexed
	book.ManifestReference = "manifests/book.pb"
	book.ManifestChecksum = sha256.Sum256([]byte("manifest"))
	repository.books[book.ID] = book

	reindexed, err := service.ReindexBook(context.Background(), book.ID, "reindex-command", validManager(), "correlation")
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := service.ReindexBook(context.Background(), book.ID, "reindex-command", validManager(), "correlation")
	if err != nil || duplicate.LifecycleVersion != reindexed.LifecycleVersion {
		t.Fatalf("duplicate = (%+v, %v)", duplicate, err)
	}

	reindexed.ProcessingStatus = BookStatusIndexed
	reindexed.ProcessingStage = BookStageIndexed
	repository.books[book.ID] = reindexed
	deleting, err := service.DeleteBook(context.Background(), book.ID, "delete-command", validManager(), "correlation")
	if err != nil {
		t.Fatal(err)
	}
	if !deleting.OriginalDeleted || deleting.ProcessingStatus != BookStatusDeleting {
		t.Fatalf("deleting = %+v", deleting)
	}
	_, _, err = repository.ApplyLifecycleAck(context.Background(), LifecycleAck{
		EventType: "ingestion.book.artifacts-deleted.v1", BookID: book.ID,
		CommandID: "delete-command", LifecycleVersion: deleting.LifecycleVersion,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	deleted, _, err := repository.ApplyLifecycleAck(context.Background(), LifecycleAck{
		EventType: "retrieval.book.index-deleted.v1", BookID: book.ID,
		CommandID: "delete-command", LifecycleVersion: deleting.LifecycleVersion,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if deleted.ProcessingStatus != BookStatusDeleted {
		t.Fatalf("deleted status = %q", deleted.ProcessingStatus)
	}
	if deleted.Metadata.Title != "" || deleted.Metadata.Author != "" || deleted.ObjectReference != "" ||
		deleted.Checksum != ([32]byte{}) || deleted.ByteSize != 0 || deleted.MediaType != "" ||
		deleted.ActorID != "" || deleted.ManifestReference != "" || deleted.ManifestChecksum != ([32]byte{}) ||
		deleted.ProcessingStage != "" || deleted.ProcessingFailureCategory != "" {
		t.Fatalf("deleted tombstone retained projections: %+v", deleted)
	}
}

func TestReindexBookAllowsEligibleFailedBook(t *testing.T) {
	repository := NewMemoryRepository()
	objects := NewMemoryObjectStore()
	service := NewServiceWithOptions(repository, objects, ServiceOptions{
		MaxBytes: 1024,
		Clock: func() time.Time {
			return time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
		},
		NewID: func() (string, error) {
			return "event-id", nil
		},
	})
	book, err := service.UploadBook(context.Background(), validUploadInput(strings.NewReader("%PDF-1.7\nbody")))
	if err != nil {
		t.Fatal(err)
	}
	book.ProcessingStatus = BookStatusFailed
	book.ProcessingStage = BookStageFailed
	book.ProcessingFailureCategory = FailureIndexingTimeout
	book.ManifestReference = "manifests/book.pb"
	book.ManifestChecksum = sha256.Sum256([]byte("manifest"))
	repository.books[book.ID] = book

	reindexed, err := service.ReindexBook(context.Background(), book.ID, "reindex-command", validManager(), "correlation")
	if err != nil {
		t.Fatalf("ReindexBook() error = %v", err)
	}
	if reindexed.ProcessingStatus != BookStatusReindexing || reindexed.ProcessingStage != BookStageChunksReady ||
		reindexed.ProcessingFailureCategory != "" {
		t.Fatalf("reindexed book = %+v", reindexed)
	}
}

func TestReindexRejectsUnusableManifestFailures(t *testing.T) {
	for _, category := range []ProcessingFailureCategory{FailureManifestIntegrity, FailureIncompatibleProfile} {
		t.Run(string(category), func(t *testing.T) {
			repository := NewMemoryRepository()
			service := NewServiceWithOptions(repository, NewMemoryObjectStore(), ServiceOptions{
				MaxBytes: 1024,
				Clock: func() time.Time {
					return time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
				},
				NewID: func() (string, error) {
					return "event-id", nil
				},
			})
			book, err := service.UploadBook(context.Background(), validUploadInput(strings.NewReader("%PDF-1.7\nbody")))
			if err != nil {
				t.Fatal(err)
			}
			book.ProcessingStatus = BookStatusFailed
			book.ProcessingStage = BookStageFailed
			book.ProcessingFailureCategory = category
			book.ManifestReference = "manifests/book.pb"
			book.ManifestChecksum = sha256.Sum256([]byte("manifest"))
			repository.books[book.ID] = book

			_, err = service.ReindexBook(context.Background(), book.ID, "reindex-command", validManager(), "correlation")
			if !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("ReindexBook() error = %v, want %v", err, ErrInvalidTransition)
			}
			if stored := repository.books[book.ID]; stored.ProcessingStatus != BookStatusFailed || stored.LifecycleVersion != book.LifecycleVersion {
				t.Fatalf("book was mutated after rejected reindex: %+v", stored)
			}
		})
	}
}

func validManager() Actor {
	return Actor{UserID: "manager", Role: "librarian", Status: "active"}
}

func TestUploadBookNormalizesAbsentTagsToEmptyArray(t *testing.T) {
	service := NewService(NewMemoryRepository(), NewMemoryObjectStore(), 1024)
	book, err := service.UploadBook(context.Background(), UploadInput{
		Metadata:      BookMetadata{Title: "A title", Author: "An author", Year: 2026},
		Actor:         Actor{UserID: "actor-1", Role: "librarian", Status: "active"},
		CorrelationID: testCorrelationID,
		Reader:        bytes.NewBufferString("%PDF-1.7\nbody"),
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
			Metadata:      BookMetadata{Title: "Title", Author: "Author", Year: 2026},
			Actor:         actor,
			CorrelationID: testCorrelationID,
			Reader:        bytes.NewBufferString("%PDF-1.7\nbody"),
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
		Metadata:      BookMetadata{Title: "Title", Author: "Author", Year: 2026},
		Actor:         Actor{UserID: "actor", Role: "librarian", Status: "active"},
		CorrelationID: testCorrelationID,
		Reader:        reader,
	}
}

func TestUploadBookRejectsMissingCorrelationBeforeReadingOrWriting(t *testing.T) {
	repository := NewMemoryRepository()
	objects := NewMemoryObjectStore()
	reader := &countingReader{reader: strings.NewReader("%PDF-1.7\nbody")}
	service := NewService(repository, objects, 1024)
	input := validUploadInput(reader)
	input.CorrelationID = ""

	_, err := service.UploadBook(context.Background(), input)

	if !errors.Is(err, ErrInvalidCorrelationID) {
		t.Fatalf("UploadBook() error = %v, want %v", err, ErrInvalidCorrelationID)
	}
	if reader.reads.Load() != 0 || len(repository.books) != 0 || len(objects.objects) != 0 {
		t.Fatalf("side effects: reads=%d books=%d objects=%d", reader.reads.Load(), len(repository.books), len(objects.objects))
	}
}

func TestUploadBookValidatesMetadataAgainstInjectedClock(t *testing.T) {
	service := NewServiceWithOptions(NewMemoryRepository(), NewMemoryObjectStore(), ServiceOptions{
		MaxBytes: 1024,
		Clock: func() time.Time {
			return time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC)
		},
	})
	input := validUploadInput(strings.NewReader("%PDF-1.7\nbody"))
	input.Metadata.Year = 2031

	book, err := service.UploadBook(context.Background(), input)

	if err != nil || book.Metadata.Year != 2031 {
		t.Fatalf("UploadBook() = (%+v, %v), want injected-clock year accepted", book, err)
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
	_, err := service.UploadBook(context.Background(), UploadInput{Metadata: BookMetadata{Title: "Title", Author: "Author", Year: 2026}, Actor: Actor{UserID: "actor", Role: "librarian", Status: "active"}, CorrelationID: testCorrelationID, Reader: bytes.NewBufferString("not a pdf")})
	if !errors.Is(err, ErrInvalidPDF) {
		t.Fatalf("error = %v", err)
	}
	if len(objects.objects) != 0 {
		t.Fatalf("objects = %d", len(objects.objects))
	}
}

func TestUploadBookEnforcesSizeLimit(t *testing.T) {
	service := NewService(NewMemoryRepository(), NewMemoryObjectStore(), 5)
	_, err := service.UploadBook(context.Background(), UploadInput{Metadata: BookMetadata{Title: "Title", Author: "Author", Year: 2026}, Actor: Actor{UserID: "actor", Role: "librarian", Status: "active"}, CorrelationID: testCorrelationID, Reader: bytes.NewBufferString("%PDF-too-large")})
	if !errors.Is(err, ErrUploadTooLarge) {
		t.Fatalf("error = %v", err)
	}
}

func TestUploadBookDeletesObjectWhenStorageReceiptDoesNotMatch(t *testing.T) {
	objects := &receiptMismatchObjectStore{objects: NewMemoryObjectStore()}
	service := NewService(NewMemoryRepository(), objects, 1024)
	_, err := service.UploadBook(context.Background(), UploadInput{
		Metadata:      BookMetadata{Title: "Title", Author: "Author", Year: 2026},
		Actor:         Actor{UserID: "actor", Role: "librarian", Status: "active"},
		CorrelationID: testCorrelationID,
		Reader:        bytes.NewBufferString("%PDF-1.7\nbody"),
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
		{name: "cancelled", err: fmt.Errorf("wrapped: %w", context.Canceled), want: context.Canceled},
		{name: "deadline", err: fmt.Errorf("wrapped: %w", context.DeadlineExceeded), want: context.DeadlineExceeded},
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
		Metadata:      BookMetadata{Title: "Title", Author: "Author", Year: 2026},
		Actor:         Actor{UserID: "actor", Role: "librarian", Status: "active"},
		CorrelationID: testCorrelationID,
		Reader:        bytes.NewBufferString("%PDF-1.7\nbody"),
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
		Metadata:      BookMetadata{Title: "Title", Author: "Author", Year: 2026},
		Actor:         Actor{UserID: "actor", Role: "librarian", Status: "active"},
		CorrelationID: testCorrelationID,
		Reader:        bytes.NewBufferString("%PDF-1.7\nbody"),
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

func (s *receiptMismatchObjectStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return s.objects.Get(ctx, key)
}

type unavailableObjectStore struct{ deleted bool }

func (s *unavailableObjectStore) Put(context.Context, string, io.Reader) (ObjectReceipt, error) {
	return ObjectReceipt{}, fmt.Errorf("minio originals/private.pdf: %w", ErrObjectStorageUnavailable)
}

func (s *unavailableObjectStore) Delete(context.Context, string) error {
	s.deleted = true
	return nil
}

func (s *unavailableObjectStore) Get(context.Context, string) (io.ReadCloser, error) {
	return nil, ErrObjectStorageUnavailable
}

func TestGetBookIncludesPreviewFromConfiguredExtractor(t *testing.T) {
	repository := NewMemoryRepository()
	objects := NewMemoryObjectStore()
	service := NewServiceWithOptions(repository, objects, ServiceOptions{
		MaxBytes:        1024,
		MaxPreviewBytes: testServiceMaxPreviewBytes,
		PreviewBook: func(_ context.Context, book Book, _ OriginalObjectStore) (string, error) {
			return "data:application/pdf;base64,ZmFrZQ==", nil
		},
	})
	book, err := service.UploadBook(context.Background(), validUploadInput(strings.NewReader("%PDF-1.7\nbody")))
	if err != nil {
		t.Fatal(err)
	}
	retrieved, err := service.GetBook(context.Background(), book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retrieved.Preview == "" || retrieved.Preview != "data:application/pdf;base64,ZmFrZQ==" {
		t.Fatalf("preview = %q", retrieved.Preview)
	}
}

func TestGetBookIgnoresPreviewFailures(t *testing.T) {
	repository := NewMemoryRepository()
	objects := NewMemoryObjectStore()
	service := NewServiceWithOptions(repository, objects, ServiceOptions{
		MaxBytes:        1024,
		MaxPreviewBytes: testServiceMaxPreviewBytes,
		PreviewBook: func(_ context.Context, book Book, _ OriginalObjectStore) (string, error) {
			return "", errors.New("preview unavailable")
		},
	})
	book, err := service.UploadBook(context.Background(), validUploadInput(strings.NewReader("%PDF-1.7\nbody")))
	if err != nil {
		t.Fatal(err)
	}
	retrieved, err := service.GetBook(context.Background(), book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retrieved.Preview != "" {
		t.Fatalf("preview = %q, want empty fallback", retrieved.Preview)
	}
}

func TestGetBookDropsOversizedPreview(t *testing.T) {
	repository := NewMemoryRepository()
	objects := NewMemoryObjectStore()
	service := NewServiceWithOptions(repository, objects, ServiceOptions{
		MaxBytes:              1024,
		MaxPreviewBytes:       testServiceMaxPreviewBytes,
		MaxPreviewPages:       testServiceMaxPreviewPages,
		MaxPreviewEPUBEntries: testServiceMaxEPUBEntries,
		PreviewBook: func(_ context.Context, _ Book, _ OriginalObjectStore) (string, error) {
			return strings.Repeat("a", testServiceMaxPreviewBytes+1), nil
		},
	})
	book, err := service.UploadBook(context.Background(), validUploadInput(strings.NewReader("%PDF-1.7\nbody")))
	if err != nil {
		t.Fatal(err)
	}

	retrieved, err := service.GetBook(context.Background(), book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retrieved.Preview != "" {
		t.Fatalf("preview = %q, want empty fallback", retrieved.Preview)
	}
}

func TestGetBookFallsBackWhenPreviewTimeoutExpires(t *testing.T) {
	repository := NewMemoryRepository()
	objects := NewMemoryObjectStore()
	service := NewServiceWithOptions(repository, objects, ServiceOptions{
		MaxBytes:        1024,
		MaxPreviewBytes: testServiceMaxPreviewBytes,
		PreviewTimeout:  50 * time.Millisecond,
		PreviewBook: func(ctx context.Context, _ Book, _ OriginalObjectStore) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	})
	book, err := service.UploadBook(context.Background(), validUploadInput(strings.NewReader("%PDF-1.7\nbody")))
	if err != nil {
		t.Fatal(err)
	}

	retrieved, err := service.GetBook(context.Background(), book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retrieved.Preview != "" {
		t.Fatalf("preview = %q, want empty fallback", retrieved.Preview)
	}
}

func TestGetBookUsesConfiguredPreviewTimeout(t *testing.T) {
	repository := NewMemoryRepository()
	objects := NewMemoryObjectStore()
	service := NewServiceWithOptions(repository, objects, ServiceOptions{
		MaxBytes:        1024,
		MaxPreviewBytes: testServiceMaxPreviewBytes,
		PreviewTimeout:  150 * time.Millisecond,
		PreviewBook: func(ctx context.Context, _ Book, _ OriginalObjectStore) (string, error) {
			select {
			case <-time.After(10 * time.Millisecond):
				return "data:application/pdf;base64,ZmFrZQ==", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
	})
	book, err := service.UploadBook(context.Background(), validUploadInput(strings.NewReader("%PDF-1.7\nbody")))
	if err != nil {
		t.Fatal(err)
	}

	retrieved, err := service.GetBook(context.Background(), book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retrieved.Preview == "" {
		t.Fatal("expected preview to be attached before the configured deadline")
	}
}

func TestGetBookBoundsPreviewConcurrency(t *testing.T) {
	repository := NewMemoryRepository()
	objects := NewMemoryObjectStore()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	service := NewServiceWithOptions(repository, objects, ServiceOptions{
		MaxBytes:           1024,
		MaxPreviewBytes:    testServiceMaxPreviewBytes,
		PreviewConcurrency: 1,
		PreviewBook: func(ctx context.Context, _ Book, _ OriginalObjectStore) (string, error) {
			calls.Add(1)
			started <- struct{}{}
			select {
			case <-release:
				return "data:application/pdf;base64,ZmFrZQ==", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
	})
	book, err := service.UploadBook(context.Background(), validUploadInput(strings.NewReader("%PDF-1.7\nbody")))
	if err != nil {
		t.Fatal(err)
	}

	firstDone := make(chan error, 1)
	go func() {
		_, getErr := service.GetBook(context.Background(), book.ID)
		firstDone <- getErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first preview did not start")
	}

	secondDone := make(chan error, 1)
	var second Book
	go func() {
		second, err = service.GetBook(context.Background(), book.ID)
		secondDone <- err
	}()
	select {
	case err = <-secondDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("second preview request blocked instead of being bounded")
	}
	if calls.Load() != 1 {
		t.Fatalf("preview calls = %d, want 1 while saturated", calls.Load())
	}
	if second.Preview != "" {
		t.Fatalf("second preview = %q, want empty fallback while saturated", second.Preview)
	}
	close(release)
	select {
	case err = <-firstDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("first preview did not complete")
	}
	if calls.Load() != 1 {
		t.Fatalf("preview calls = %d, want 1", calls.Load())
	}
}

func TestListBooksRejectsMalformedCursorAsPagination(t *testing.T) {
	service := NewService(NewMemoryRepository(), NewMemoryObjectStore(), 1024)
	_, _, err := service.ListBooks(context.Background(), 25, "not-a-cursor")
	if !errors.Is(err, ErrInvalidPagination) {
		t.Fatalf("error = %v", err)
	}
}

func TestListBooksUsesConfiguredDefaultAndPaginationLimits(t *testing.T) {
	repository := NewMemoryRepository()
	service := NewServiceWithOptions(repository, NewMemoryObjectStore(), ServiceOptions{
		MaxBytes:             1024,
		ListPageDefaultSize:  2,
		ListPageMaxSize:      3,
		ListPageTokenMaxSize: 8,
	})
	for _, id := range []string{"a", "b", "c"} {
		if err := repository.Create(context.Background(), Book{
			ID:        id,
			Metadata:  BookMetadata{Title: id, Author: "author"},
			Checksum:  [32]byte{1},
			CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	books, _, err := service.ListBooks(context.Background(), 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 2 {
		t.Fatalf("default page length = %d, want 2", len(books))
	}
	_, _, err = service.ListBooks(context.Background(), 4, "")
	if !errors.Is(err, ErrInvalidPagination) {
		t.Fatalf("oversized page error = %v", err)
	}
	_, _, err = service.ListBooks(context.Background(), 1, strings.Repeat("a", 9))
	if !errors.Is(err, ErrInvalidPagination) {
		t.Fatalf("oversized token error = %v", err)
	}
}
