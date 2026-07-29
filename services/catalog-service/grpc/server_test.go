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

func TestContextErrorsKeepTheirGRPCStatusAndDiagnosticReason(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		err        error
		wantCode   codes.Code
		wantReason string
	}{
		{name: "cancelled", err: fmt.Errorf("wrapped: %w", context.Canceled), wantCode: codes.Canceled, wantReason: "request_cancelled"},
		{name: "deadline", err: fmt.Errorf("wrapped: %w", context.DeadlineExceeded), wantCode: codes.DeadlineExceeded, wantReason: "deadline_exceeded"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := status.Code(mapError(testCase.err)); got != testCase.wantCode {
				t.Fatalf("mapError() code = %v, want %v", got, testCase.wantCode)
			}
			if got := uploadFailureReason(testCase.err); got != testCase.wantReason {
				t.Fatalf("uploadFailureReason() = %q, want %q", got, testCase.wantReason)
			}
		})
	}
}

func TestUploadBookRejectsMissingRequestIDBeforeReceivingBody(t *testing.T) {
	service := catalog.NewServiceWithOptions(fakeBookRepository{}, nil, catalog.ServiceOptions{MaxBytes: 1024})
	server := NewServer(service, diagnostic.New(zap.NewNop()), Policy{
		PreviewTimeout:        time.Second,
		ReadinessProbeTimeout: time.Second,
		UploadTimeout:         time.Second,
		LifecycleTimeout:      time.Second,
		ListTimeout:           time.Second,
	})
	stream := &noRecvUploadStream{ctx: context.Background()}

	err := server.UploadBook(stream)

	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("UploadBook() code = %v, want %v", status.Code(err), codes.InvalidArgument)
	}
	if stream.received {
		t.Fatal("UploadBook() received body before validating request ID")
	}
}

func TestCheckPreservesReadinessContextErrors(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		err      error
		wantCode codes.Code
	}{
		{name: "cancelled", err: fmt.Errorf("wrapped: %w", context.Canceled), wantCode: codes.Canceled},
		{name: "deadline", err: fmt.Errorf("wrapped: %w", context.DeadlineExceeded), wantCode: codes.DeadlineExceeded},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			service := catalog.NewServiceWithOptions(fakeBookRepository{}, nil, catalog.ServiceOptions{MaxBytes: 1024})
			server := NewServer(service, diagnostic.New(zap.NewNop()), Policy{
				PreviewTimeout:        time.Second,
				ReadinessProbeTimeout: time.Second,
				UploadTimeout:         time.Second,
				LifecycleTimeout:      time.Second,
				ListTimeout:           time.Second,
			}, readinessError{err: testCase.err})

			_, err := server.Check(context.Background(), &catalogv1.CheckRequest{})

			if status.Code(err) != testCase.wantCode {
				t.Fatalf("Check() code = %v, want %v", status.Code(err), testCase.wantCode)
			}
		})
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

type noRecvUploadStream struct {
	catalogv1.CatalogService_UploadBookServer
	ctx      context.Context
	received bool
}

type readinessError struct {
	err error
}

func (r readinessError) CheckReady(context.Context) error {
	return r.err
}

func (s *noRecvUploadStream) Context() context.Context {
	return s.ctx
}

func (s *noRecvUploadStream) Recv() (*catalogv1.UploadBookRequest, error) {
	s.received = true
	return nil, errors.New("unexpected receive")
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
