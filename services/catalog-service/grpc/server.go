// Package cataloggrpc adapts Catalog's gRPC contract to its application service.
package cataloggrpc

import (
	"bytes"
	"context"
	"errors"
	"io"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
	"github.com/belLena81/raglibrarian/services/catalog-service/internal/catalog"
)

type Server struct {
	catalogv1.UnimplementedCatalogServiceServer
	service *catalog.Service
}

func NewServer(service *catalog.Service) *Server {
	if service == nil {
		panic("cataloggrpc: service is required")
	}
	return &Server{service: service}
}

func (*Server) Check(ctx context.Context, _ *catalogv1.CheckRequest) (*catalogv1.CheckResponse, error) {
	if ctx.Err() != nil {
		return nil, status.Error(codes.Canceled, "request cancelled")
	}
	return &catalogv1.CheckResponse{Status: "SERVING"}, nil
}

func (s *Server) UploadBook(stream catalogv1.CatalogService_UploadBookServer) error {
	first, err := stream.Recv()
	if err != nil {
		return mapError(err)
	}
	metadata := first.GetMetadata()
	if metadata == nil {
		return status.Error(codes.InvalidArgument, "invalid upload")
	}
	reader := &chunkReader{stream: stream}
	book, err := s.service.UploadBook(stream.Context(), catalog.UploadInput{
		Metadata: catalog.BookMetadata{Title: metadata.Title, Author: metadata.Author, Year: int(metadata.Year), Tags: append([]string(nil), metadata.Tags...)},
		ActorID:  metadata.ActorId, CorrelationID: metadata.CorrelationId, Reader: reader,
	})
	if err != nil {
		return mapError(err)
	}
	return stream.SendAndClose(&catalogv1.UploadBookResponse{Book: bookProto(book)})
}

func (s *Server) ListBooks(ctx context.Context, request *catalogv1.ListBooksRequest) (*catalogv1.ListBooksResponse, error) {
	books, token, err := s.service.ListBooks(ctx, int(request.PageSize), request.PageToken)
	if err != nil {
		return nil, mapError(err)
	}
	response := &catalogv1.ListBooksResponse{NextPageToken: token, Books: make([]*catalogv1.Book, 0, len(books))}
	for _, book := range books {
		response.Books = append(response.Books, bookProto(book))
	}
	return response, nil
}

func (s *Server) GetBook(ctx context.Context, request *catalogv1.GetBookRequest) (*catalogv1.GetBookResponse, error) {
	book, err := s.service.GetBook(ctx, request.BookId)
	if err != nil {
		return nil, mapError(err)
	}
	return &catalogv1.GetBookResponse{Book: bookProto(book)}, nil
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
		if chunk == nil || len(chunk) == 0 || len(chunk) > catalog.ChunkSize || request.GetMetadata() != nil {
			return 0, catalog.ErrInvalidStream
		}
		r.buffer = bytes.NewReader(chunk)
	}
	return r.buffer.Read(target)
}

func bookProto(book catalog.Book) *catalogv1.Book {
	return &catalogv1.Book{Id: book.ID, Title: book.Metadata.Title, Author: book.Metadata.Author, Year: int32(book.Metadata.Year), Tags: append([]string(nil), book.Metadata.Tags...), ProcessingStatus: string(book.ProcessingStatus), CreatedAt: timestamppb.New(book.CreatedAt)}
}

func mapError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "request cancelled")
	case errors.Is(err, catalog.ErrInvalidMetadata), errors.Is(err, catalog.ErrInvalidPDF), errors.Is(err, catalog.ErrInvalidStream):
		return status.Error(codes.InvalidArgument, "invalid upload")
	case errors.Is(err, catalog.ErrUploadTooLarge):
		return status.Error(codes.ResourceExhausted, "upload too large")
	case errors.Is(err, catalog.ErrNotFound):
		return status.Error(codes.NotFound, "book not found")
	case errors.Is(err, io.EOF):
		return status.Error(codes.InvalidArgument, "invalid upload")
	default:
		return status.Error(codes.Unavailable, "catalog unavailable")
	}
}
