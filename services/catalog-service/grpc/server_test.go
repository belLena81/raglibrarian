package cataloggrpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
	"github.com/belLena81/raglibrarian/services/catalog-service/diagnostic"
	"github.com/belLena81/raglibrarian/services/catalog-service/internal/catalog"
)

func TestStorageFailuresUseAllowlistedDiagnosticsAndSanitizedUnavailableStatus(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		err        error
		wantReason string
	}{
		{name: "storage unavailable", err: fmt.Errorf("put original: %w", catalog.ErrObjectStorageUnavailable), wantReason: "object_storage_unavailable"},
		{name: "receipt mismatch", err: fmt.Errorf("verify original: %w", catalog.ErrObjectReceiptMismatch), wantReason: "object_receipt_mismatch"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := uploadFailureReason(testCase.err); got != testCase.wantReason {
				t.Fatalf("uploadFailureReason() = %q, want %q", got, testCase.wantReason)
			}
			mapped := mapError(testCase.err)
			if status.Code(mapped) != codes.Unavailable || status.Convert(mapped).Message() != "catalog unavailable" {
				t.Fatalf("mapError() = %v", mapped)
			}
			if strings.Contains(status.Convert(mapped).Message(), "originals/") {
				t.Fatal("mapped error exposed object reference")
			}
		})
	}
}

func TestUnknownFailureRemainsPersistenceUnavailable(t *testing.T) {
	if got := uploadFailureReason(errors.New("database connection lost")); got != "persistence_unavailable" {
		t.Fatalf("uploadFailureReason() = %q", got)
	}
}

func TestProcessingEventConflictMapsToLifecycleConflict(t *testing.T) {
	mapped := mapError(catalog.ErrProcessingEventConflict)
	if status.Code(mapped) != codes.Aborted || status.Convert(mapped).Message() != "lifecycle conflict" {
		t.Fatalf("mapError() = %v", mapped)
	}
}

func TestGetBookUsesConfiguredPreviewTimeout(t *testing.T) {
	configuredPreviewTimeout := 8 * time.Second
	repository := fakeBookRepository{book: catalog.Book{
		ID:               "book-1",
		ProcessingStatus: catalog.BookStatusIndexed,
		ObjectReference:  "object-1",
	}}
	deadlineObserved := make(chan time.Duration, 1)
	service := catalog.NewServiceWithOptions(repository, nil, catalog.ServiceOptions{
		MaxBytes:        1024,
		MaxPreviewBytes: 1024,
		PreviewTimeout:  configuredPreviewTimeout,
		PreviewBook: func(ctx context.Context, _ catalog.Book, _ catalog.OriginalObjectStore) (string, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				return "", errors.New("missing deadline")
			}
			deadlineObserved <- time.Until(deadline)
			return "", nil
		},
	})
	server := NewServer(service, diagnostic.New(zap.NewNop()), Policy{
		PreviewTimeout:        configuredPreviewTimeout,
		ReadinessProbeTimeout: 2 * time.Second,
		UploadTimeout:         2 * time.Minute,
		LifecycleTimeout:      10 * time.Second,
		ListTimeout:           5 * time.Second,
	})
	response, err := server.GetBook(context.Background(), &catalogv1.GetBookRequest{
		BookId: "book-1",
		Actor:  &catalogv1.Actor{UserId: "reader-1", Role: "reader", Status: "active", MaskedEmail: "reader@example.com"},
	})
	if err != nil {
		t.Fatalf("GetBook() = %v", err)
	}
	if got := response.GetBook().GetId(); got != "book-1" {
		t.Fatalf("GetBook response Book.Id = %q", got)
	}
	deadline := <-deadlineObserved
	if deadline <= configuredPreviewTimeout-500*time.Millisecond {
		t.Fatalf("preview deadline %v, want >= %v", deadline, configuredPreviewTimeout)
	}
}

type fakeBookRepository struct {
	book catalog.Book
}

func (f fakeBookRepository) Create(context.Context, catalog.Book, ...catalog.OutboxEvent) error {
	return nil
}

func (f fakeBookRepository) List(context.Context, int, string) ([]catalog.Book, string, error) {
	return nil, "", nil
}

func (f fakeBookRepository) Get(context.Context, string) (catalog.Book, error) {
	return f.book, nil
}
