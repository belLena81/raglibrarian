// Package cataloggrpc adapts Catalog's gRPC contract to its application service.
package cataloggrpc

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
	"github.com/belLena81/raglibrarian/services/catalog-service/diagnostic"
	"github.com/belLena81/raglibrarian/services/catalog-service/internal/catalog"
)

type Server struct {
	catalogv1.UnimplementedCatalogServiceServer
	service     *catalog.Service
	diagnostics *diagnostic.Recorder
	readiness   interface{ CheckReady(context.Context) error }
}

func NewServer(service *catalog.Service, diagnostics *diagnostic.Recorder, readiness ...interface{ CheckReady(context.Context) error }) *Server {
	if service == nil || diagnostics == nil {
		panic("cataloggrpc: service and diagnostics are required")
	}
	server := &Server{service: service, diagnostics: diagnostics}
	if len(readiness) == 1 {
		server.readiness = readiness[0]
	}
	return server
}

func (s *Server) Check(ctx context.Context, _ *catalogv1.CheckRequest) (*catalogv1.CheckResponse, error) {
	if ctx.Err() != nil {
		return nil, status.Error(codes.Canceled, "request cancelled")
	}
	if s.readiness != nil {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := s.readiness.CheckReady(probeCtx); err != nil {
			return nil, status.Error(codes.Unavailable, "catalog unavailable")
		}
	}
	return &catalogv1.CheckResponse{Status: "SERVING"}, nil
}

func (s *Server) UploadBook(stream catalogv1.CatalogService_UploadBookServer) error {
	ctx, cancel := context.WithTimeout(stream.Context(), 2*time.Minute)
	defer cancel()
	first, err := stream.Recv()
	if err != nil {
		s.diagnostics.OperationRejected("upload_book", requestIDFromMetadata(ctx), catalog.Actor{}, uploadFailureReason(err))
		return mapError(err)
	}
	metadata := first.GetMetadata()
	if metadata == nil {
		s.diagnostics.OperationRejected("upload_book", requestIDFromMetadata(ctx), catalog.Actor{}, "invalid_metadata")
		return status.Error(codes.InvalidArgument, "invalid upload")
	}
	reader := &chunkReader{stream: stream}
	actor := actorFromProto(metadata.Actor)
	if !actor.CanUpload() {
		s.diagnostics.OperationRejected("upload_book", requestIDFromMetadata(ctx), actor, "unauthorized_actor")
		return status.Error(codes.PermissionDenied, "actor is not authorized")
	}
	book, err := s.service.UploadBook(ctx, catalog.UploadInput{
		Metadata: catalog.BookMetadata{Title: metadata.Title, Author: metadata.Author, Year: int(metadata.Year), Tags: append([]string(nil), metadata.Tags...)},
		Actor:    actor, CorrelationID: requestIDFromMetadata(ctx), Reader: reader,
	})
	if err != nil {
		s.diagnostics.OperationRejected("upload_book", requestIDFromMetadata(ctx), actor, uploadFailureReason(err))
		return mapError(err)
	}
	s.diagnostics.UploadCompleted(requestIDFromMetadata(ctx), actor, book)
	return stream.SendAndClose(&catalogv1.UploadBookResponse{Book: bookProto(book)})
}

func requestIDFromMetadata(ctx context.Context) string {
	values := metadata.ValueFromIncomingContext(ctx, "x-request-id")
	if len(values) != 1 || !validRequestID(values[0]) {
		return ""
	}
	return values[0]
}

func validRequestID(value string) bool {
	if len(value) != 32 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16
}

func (s *Server) ListBooks(ctx context.Context, request *catalogv1.ListBooksRequest) (*catalogv1.ListBooksResponse, error) {
	actor := actorFromProto(request.Actor)
	requestID := requestIDFromMetadata(ctx)
	if !actor.CanRead() {
		s.diagnostics.OperationRejected("list_books", requestID, actor, "unauthorized_actor")
		return nil, status.Error(codes.PermissionDenied, "actor is not authorized")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	books, token, err := s.service.ListBooks(ctx, int(request.PageSize), request.PageToken)
	if err != nil {
		s.diagnostics.OperationRejected("list_books", requestID, actor, uploadFailureReason(err))
		return nil, mapError(err)
	}
	response := &catalogv1.ListBooksResponse{NextPageToken: token, Books: make([]*catalogv1.Book, 0, len(books))}
	for _, book := range books {
		response.Books = append(response.Books, bookProto(book))
	}
	s.diagnostics.ListCompleted(requestID, actor, int(request.PageSize), len(books))
	return response, nil
}

func (s *Server) GetBook(ctx context.Context, request *catalogv1.GetBookRequest) (*catalogv1.GetBookResponse, error) {
	actor := actorFromProto(request.Actor)
	requestID := requestIDFromMetadata(ctx)
	if !actor.CanRead() {
		s.diagnostics.OperationRejected("get_book", requestID, actor, "unauthorized_actor")
		return nil, status.Error(codes.PermissionDenied, "actor is not authorized")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	book, err := s.service.GetBook(ctx, request.BookId)
	if err != nil {
		s.diagnostics.OperationRejected("get_book", requestID, actor, uploadFailureReason(err))
		return nil, mapError(err)
	}
	s.diagnostics.GetCompleted(requestID, actor, book.ID)
	return &catalogv1.GetBookResponse{Book: bookProto(book)}, nil
}

func uploadFailureReason(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "request_cancelled"
	case errors.Is(err, catalog.ErrInvalidMetadata):
		return "invalid_metadata"
	case errors.Is(err, catalog.ErrUnauthorizedActor):
		return "unauthorized_actor"
	case errors.Is(err, catalog.ErrInvalidPDF):
		return "invalid_pdf"
	case errors.Is(err, catalog.ErrInvalidStream), errors.Is(err, io.EOF):
		return "invalid_stream"
	case errors.Is(err, catalog.ErrUploadTooLarge):
		return "upload_too_large"
	case errors.Is(err, catalog.ErrUploadCapacity):
		return "upload_capacity_exhausted"
	case errors.Is(err, catalog.ErrObjectStorageUnavailable):
		return "object_storage_unavailable"
	case errors.Is(err, catalog.ErrObjectReceiptMismatch):
		return "object_receipt_mismatch"
	case errors.Is(err, catalog.ErrInvalidPagination):
		return "invalid_pagination"
	case errors.Is(err, catalog.ErrNotFound):
		return "not_found"
	default:
		return "persistence_unavailable"
	}
}

func actorFromProto(actor *catalogv1.Actor) catalog.Actor {
	if actor == nil {
		return catalog.Actor{}
	}
	return catalog.Actor{UserID: actor.UserId, Role: actor.Role, Status: actor.Status, MaskedEmail: actor.MaskedEmail}
}

type chunkReader struct {
	stream catalogv1.CatalogService_UploadBookServer
	buffer *bytes.Reader
}

func (r *chunkReader) Read(target []byte) (int, error) {
	for r.buffer == nil || r.buffer.Len() == 0 {
		request, err := r.stream.Recv()
		if errors.Is(err, io.EOF) {
			return 0, io.EOF
		}
		if err != nil {
			return 0, err
		}
		chunk := request.GetChunk()
		if len(chunk) == 0 || len(chunk) > catalog.ChunkSize || request.GetMetadata() != nil {
			return 0, catalog.ErrInvalidStream
		}
		r.buffer = bytes.NewReader(chunk)
	}
	return r.buffer.Read(target)
}

func bookProto(book catalog.Book) *catalogv1.Book {
	return &catalogv1.Book{
		Id: book.ID, Title: book.Metadata.Title, Author: book.Metadata.Author, Year: int32(book.Metadata.Year), // #nosec G115 -- Catalog validates years before persistence.
		Tags: append([]string(nil), book.Metadata.Tags...), ProcessingStatus: string(book.ProcessingStatus),
		CreatedAt: timestamppb.New(book.CreatedAt), ProcessingStage: string(book.ProcessingStage),
		ProcessingFailureCategory: string(book.ProcessingFailureCategory),
		ProcessingUpdatedAt:       timestamppb.New(book.ProcessingUpdatedAt), ProcessingVersion: book.ProcessingVersion,
	}
}

func mapError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "request cancelled")
	case errors.Is(err, catalog.ErrInvalidPagination):
		return status.Error(codes.InvalidArgument, "invalid pagination")
	case errors.Is(err, catalog.ErrInvalidMetadata), errors.Is(err, catalog.ErrInvalidPDF), errors.Is(err, catalog.ErrInvalidStream):
		return status.Error(codes.InvalidArgument, "invalid upload")
	case errors.Is(err, catalog.ErrUnauthorizedActor):
		return status.Error(codes.PermissionDenied, "actor is not authorized")
	case errors.Is(err, catalog.ErrUploadTooLarge):
		return status.Error(codes.ResourceExhausted, "upload too large")
	case errors.Is(err, catalog.ErrUploadCapacity):
		return status.Error(codes.ResourceExhausted, "upload capacity exhausted")
	case errors.Is(err, catalog.ErrNotFound):
		return status.Error(codes.NotFound, "book not found")
	case errors.Is(err, io.EOF):
		return status.Error(codes.InvalidArgument, "invalid upload")
	default:
		return status.Error(codes.Unavailable, "catalog unavailable")
	}
}
