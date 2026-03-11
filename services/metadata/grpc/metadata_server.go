// Package grpc contains the gRPC server for the metadata service.
package grpc

import (
	"context"
	"math"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/belLena81/raglibrarian/pkg/domain"
	"github.com/belLena81/raglibrarian/pkg/proto/metadatapb"
	metarepo "github.com/belLena81/raglibrarian/services/metadata/repository"
)

// BookUseCaseInterface is the subset of usecase.BookUseCase that MetadataServer
// requires. Declaring it here rather than importing the usecase package directly
// keeps the gRPC layer free of any circular dependency and makes the fake in
// tests trivial to write.
type BookUseCaseInterface interface {
	GetBook(ctx context.Context, id string) (domain.Book, error)
	UpdateStatus(ctx context.Context, id string, next domain.Status) error
	// The remaining BookUseCase methods are not exposed via gRPC in this step;
	// the interface grows here as new RPCs are added to the proto file.
	AddBook(ctx context.Context, title, author string, year int) (domain.Book, error)
	ListBooks(ctx context.Context, f metarepo.ListFilter) ([]domain.Book, error)
	RemoveBook(ctx context.Context, id string) error
	TriggerReindex(ctx context.Context, id string) error
}

// MetadataServer implements metadatapb.MetadataServiceServer.
// It translates gRPC requests into use-case calls and maps domain errors to
// gRPC status codes via error_mapping.go.
type MetadataServer struct {
	metadatapb.UnimplementedMetadataServiceServer
	books BookUseCaseInterface
}

// NewMetadataServer constructs a MetadataServer. Panics if uc is nil.
func NewMetadataServer(uc BookUseCaseInterface) *MetadataServer {
	if uc == nil {
		panic("grpc: BookUseCaseInterface must not be nil")
	}
	return &MetadataServer{books: uc}
}

// UpdateBookStatus advances the index pipeline state for a book.
// Blank book_id or status fields are rejected before reaching the use case.
func (s *MetadataServer) UpdateBookStatus(ctx context.Context, req *metadatapb.UpdateBookStatusRequest) (*metadatapb.UpdateBookStatusResponse, error) {
	if strings.TrimSpace(req.GetBookId()) == "" {
		return nil, status.Error(codes.InvalidArgument, domain.ErrEmptyBookID.Error())
	}
	if strings.TrimSpace(req.GetStatus()) == "" {
		return nil, status.Error(codes.InvalidArgument, "status must not be empty")
	}

	next, err := domain.StatusValueOf(req.GetStatus())
	if err != nil {
		// Unrecognised status string — map before hitting the use case so the
		// error code is InvalidArgument, not whatever the use case would return.
		return nil, status.Error(codes.InvalidArgument, domain.ErrInvalidStatus.Error())
	}

	if err = s.books.UpdateStatus(ctx, req.GetBookId(), next); err != nil {
		return nil, domainToStatus(err)
	}

	return &metadatapb.UpdateBookStatusResponse{
		BookId: req.GetBookId(),
		Status: next.String(),
	}, nil
}

// GetBook returns a book by ID.
// Blank book_id is rejected before reaching the use case.
func (s *MetadataServer) GetBook(ctx context.Context, req *metadatapb.GetBookRequest) (*metadatapb.GetBookResponse, error) {
	if strings.TrimSpace(req.GetBookId()) == "" {
		return nil, status.Error(codes.InvalidArgument, domain.ErrEmptyBookID.Error())
	}

	book, err := s.books.GetBook(ctx, req.GetBookId())
	if err != nil {
		return nil, domainToStatus(err)
	}

	return &metadatapb.GetBookResponse{Book: domainToBookProto(book)}, nil
}

// domainToBookProto converts a domain.Book to its wire representation.
// Tags nil-guard: proto3 repeated string fields serialise a nil Go slice and
// an empty Go slice identically on the wire, but callers must not receive a
// nil pointer for a field they will range over — so we leave nil as-is here
// and document that callers treat nil Tags as empty.
func domainToBookProto(b domain.Book) *metadatapb.BookProto {
	return &metadatapb.BookProto{
		BookId:    b.Id(),
		Title:     b.Title(),
		Author:    b.Author(),
		Year:      safeInt32(b.Year()),
		Status:    b.Status().String(),
		Tags:      b.Tags(),
		S3Key:     b.S3Key(),
		CreatedAt: b.CreatedAt().Unix(),
		UpdatedAt: b.UpdatedAt().Unix(),
	}
}

// safeInt32 converts n to int32, clamping to [math.MinInt32, math.MaxInt32] on
// platforms where int is 64-bit. For book publication years the value is always
// well within range, but an explicit guard satisfies gosec G115 and makes the
// contract visible at the call site.
func safeInt32(n int) int32 {
	switch {
	case n > math.MaxInt32:
		return math.MaxInt32
	case n < math.MinInt32:
		return math.MinInt32
	default:
		return int32(n)
	}
}
